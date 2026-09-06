// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package parser provides filtering rules if you need.
package parser

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	pgparser "github.com/auxten/postgresql-parser/pkg/sql/parser"
	"github.com/auxten/postgresql-parser/pkg/sql/sem/tree"
	"github.com/golang/glog"
)

// passwordLiteralRe matches a quoted string literal immediately following
// PASSWORD or IDENTIFIED BY (case-insensitive), e.g. the secret in
// `ALTER ROLE bob WITH PASSWORD 'hunter2'` or
// `CREATE USER bob IDENTIFIED BY 'hunter2'`. Used by redactSecrets below.
//
// This is a best-effort textual redaction, not a full-grammar one: it
// cannot recognize a secret spread across dollar-quoting, string
// concatenation, or a bind parameter, and it only knows the two keywords
// above. Do not treat on_parse_error="audit" logs as safe to hand out
// broadly - see REVIEW.md M9 and the doc comment on OnParseError's audit
// log call below.
var passwordLiteralRe = regexp.MustCompile(`(?is)(PASSWORD\s+|IDENTIFIED\s+BY\s+)'(?:[^'\\]|\\.)*'`)

// redactSecrets replaces PASSWORD/IDENTIFIED BY string literals in query
// with a fixed placeholder, so that logging query as part of audit output
// doesn't leak plaintext credentials into the log stream (AGENTS.md forbids
// logging secrets). See passwordLiteralRe's doc comment for the limits of
// this redaction.
func redactSecrets(query string) string {
	return passwordLiteralRe.ReplaceAllString(query, "${1}'***REDACTED***'")
}

// FilterConfig holds configurable rules for filtering SQL queries.
type FilterConfig struct {
	AllowSelect           bool `toml:"allow_select"`
	AllowInsert           bool `toml:"allow_insert"`
	AllowUpdate           bool `toml:"allow_update"`
	AllowDelete           bool `toml:"allow_delete"`
	AllowTruncate         bool `toml:"allow_truncate"`
	AllowAlterRole        bool `toml:"allow_alter_role"`
	AllowSetVar           bool `toml:"allow_set_var"`
	AllowExecute          bool `toml:"allow_execute"`
	RequireWhereForUpdate bool `toml:"require_where_for_update"`
	RequireWhereForDelete bool `toml:"require_where_for_delete"`

	// BlockSetVars lists GUC names that are always blocked - even when
	// AllowSetVar is true - because setting them mid-session lets a
	// client re-authenticate as another role or otherwise change its
	// effective privileges (see REVIEW.md M1). Matched case-insensitively
	// against the SET statement's variable name. Defaults to
	// {"session_authorization", "role"}; only takes effect when
	// AllowSetVar is true (AllowSetVar=false already blocks every SET).
	BlockSetVars []string `toml:"block_set_vars"`

	SignatureFilterEnabled  bool   `toml:"signature_filter_enabled"`
	SignatureAllowByDefault bool   `toml:"signature_allow_by_default"`
	SignatureAuditMode      bool   `toml:"signature_audit_mode"`
	OnParseError            string `toml:"on_parse_error"`

	BlockSignatures []string `toml:"block_signatures"`
	AllowSignatures []string `toml:"allow_signatures"`
}

// DefaultFilterConfig returns a secure default configuration.
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		AllowSelect:    true,
		AllowInsert:    true,
		AllowUpdate:    true,
		AllowDelete:    true,
		AllowTruncate:  false,
		AllowAlterRole: false,
		// AllowSetVar defaults to true (REVIEW.md M1): with it false,
		// every SET statement is blocked, including init statements
		// drivers/ORMs send unconditionally at connect time
		// (pgjdbc's "SET extra_float_digits = 3", ActiveRecord's "SET
		// client_min_messages", "SET TIME ZONE", ...) - a
		// general-purpose proxy that breaks every common client out of
		// the box is not a usable secure default. The privilege-relevant
		// GUCs (session_authorization, role) are still blocked via
		// BlockSetVars regardless of this setting.
		AllowSetVar:           true,
		BlockSetVars:          []string{"session_authorization", "role"},
		AllowExecute:          false,
		RequireWhereForUpdate: true,
		RequireWhereForDelete: true,
	}
}

// QueryFilter applies FilterConfig rules to SQL statements.
type QueryFilter struct {
	config FilterConfig
}

// NewQueryFilter creates a new QueryFilter with the given configuration.
func NewQueryFilter(config FilterConfig) *QueryFilter {
	WarnIfFilterConfigIsUnsafe(config, glog.Warningf)
	return &QueryFilter{config: config}
}

// WarnIfFilterConfigIsUnsafe logs a loud warning (via warnf, e.g.
// glog.Warningf) when the signature-filter configuration blocks every
// statement: signature_filter_enabled is true,
// signature_allow_by_default is false, and allow_signatures is empty. That
// combination is almost certainly a configuration mistake rather than an
// intentional "block everything" firewall.
func WarnIfFilterConfigIsUnsafe(config FilterConfig, warnf func(format string, args ...interface{})) {
	if config.SignatureFilterEnabled && !config.SignatureAllowByDefault && len(config.AllowSignatures) == 0 {
		warnf("Filter config: signature_filter_enabled=true, signature_allow_by_default=false and " +
			"allow_signatures is empty - EVERY statement will be blocked. Add entries to " +
			"allow_signatures, set signature_allow_by_default=true, or set " +
			"signature_filter_enabled=false.")
	}
}

// Filter checks if the SQL statement meets the configured criteria.
// Returns true if the query is safe and should be allowed.

// extractStatements returns stmt plus every tree.Statement reachable from it
// by generic reflection over exported fields, slices and interfaces: CTEs
// (top-level and nested arbitrarily deep), subqueries anywhere a Select can
// appear (FROM, JOIN, scalar/EXISTS/IN expressions, INSERT sources, UPDATE
// SET expressions, CREATE TABLE AS), and EXPLAIN/PREPARE bodies. Walking the
// AST generically - rather than hand-listing every statement/expression
// field that might hold a nested statement - means a bypass shape the
// switch below doesn't special-case still gets classified, because the
// walk finds the nested Insert/Update/Delete/Truncate node regardless of
// where it is embedded (see REVIEW.md C1).
//
// A pointer's identity is tracked in seen to avoid re-visiting shared nodes
// (harmless for an AST, which is a tree, but cheap insurance against
// accidental sharing or future library changes) and to bound the walk.
func extractStatements(root tree.Statement) []tree.Statement {
	out := []tree.Statement{root}
	seen := make(map[uintptr]bool)
	v := reflect.ValueOf(root)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return out
		}
		seen[v.Pointer()] = true
		walkStatementTree(v.Elem(), seen, &out)
	} else {
		walkStatementTree(v, seen, &out)
	}
	return out
}

func walkStatementTree(v reflect.Value, seen map[uintptr]bool, out *[]tree.Statement) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		walkStatementTree(v.Elem(), seen, out)
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		ptr := v.Pointer()
		if seen[ptr] {
			return
		}
		seen[ptr] = true
		if stmt, ok := v.Interface().(tree.Statement); ok {
			*out = append(*out, stmt)
		}
		walkStatementTree(v.Elem(), seen, out)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported field
			}
			walkStatementTree(v.Field(i), seen, out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStatementTree(v.Index(i), seen, out)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walkStatementTree(v.MapIndex(k), seen, out)
		}
	}
}

func (f *QueryFilter) Filter(str []byte) bool {
	stmts, err := pgparser.Parse(string(str))
	if err != nil {
		glog.Errorf("Parse error: %v", err)
		if f.config.OnParseError == "allow" || f.config.OnParseError == "audit" {
			if f.config.OnParseError == "audit" {
				// Unlike SIGNATURE_AUDIT below (which logs a
				// constants-hidden signature), a query that failed to
				// parse has no AST to redact via FmtHideConstants, so the
				// raw text is logged - best-effort redact PASSWORD/
				// IDENTIFIED BY literals first (M9).
				glog.Warningf("AUDIT (Parse Error): %s", redactSecrets(string(str)))
			}
			return true
		}
		return false
	}

	for _, stmt := range stmts {
		// Signature-based filtering
		sig := tree.AsStringWithFlags(stmt.AST, tree.FmtHideConstants)

		if f.config.SignatureFilterEnabled {
			blocked := false

			// Check block signatures first
			for _, b := range f.config.BlockSignatures {
				if sig == b {
					blocked = true
					break
				}
			}

			// If not blocked by blocklist, check allow logic
			if !blocked {
				allowed := false
				for _, a := range f.config.AllowSignatures {
					if sig == a {
						allowed = true
						break
					}
				}

				if !allowed {
					// If it wasn't explicitly allowed, fallback to default behavior
					if !f.config.SignatureAllowByDefault {
						blocked = true
					}
				}
			}

			if blocked {
				if f.config.SignatureAuditMode {
					glog.Infof("SIGNATURE_AUDIT: %q", sig)
				} else {
					return false
				}
			}
		}

		for _, ast := range extractStatements(stmt.AST) {
			switch ast := ast.(type) {
			case *tree.Select:
				if !f.config.AllowSelect {
					return false
				}
			case *tree.Delete:
				if !f.config.AllowDelete {
					return false
				}
				if f.config.RequireWhereForDelete && ast.Where == nil {
					return false
				}
			case *tree.Update:
				if !f.config.AllowUpdate {
					return false
				}
				if f.config.RequireWhereForUpdate && ast.Where == nil {
					return false
				}
			case *tree.Insert:
				if !f.config.AllowInsert {
					return false
				}
			case *tree.Truncate:
				if !f.config.AllowTruncate {
					return false
				}

			case *tree.AlterRole:
				if !f.config.AllowAlterRole {
					return false
				}
			case *tree.SetVar:
				if !f.config.AllowSetVar {
					return false
				}
				for _, blocked := range f.config.BlockSetVars {
					if strings.EqualFold(ast.Name, blocked) {
						return false
					}
				}
			case *tree.Execute:
				if !f.config.AllowExecute {
					return false
				}
			default:
				glog.V(2).Infof("Allowing %T by default", ast)
			}
		}
	}
	return true
}

// Handler is a proxy Handler that filters queries based on the configuration.
func (f *QueryFilter) Handler(query string) ([]byte, error) {
	if !f.Filter([]byte(query)) {
		return nil, fmt.Errorf("query blocked by filter")
	}
	return []byte(query), nil
}
