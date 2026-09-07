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
	"sync/atomic"

	"github.com/golang/glog"
	pgquery "github.com/pganalyze/pg_query_go/v6"
)

// passwordLiteralRe matches a quoted string literal immediately following
// PASSWORD or IDENTIFIED BY (case-insensitive), e.g. the secret in
// `ALTER ROLE bob WITH PASSWORD 'hunter2'` or
// `CREATE USER bob IDENTIFIED BY 'hunter2'`. Used by redactSecrets below,
// as the fallback for the (now rare - see OnParseError's doc comment)
// statement that fails to parse at all, so there is no AST to Normalize.
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
	// against the SET/RESET statement's variable name. Defaults to
	// {"session_authorization", "role"}; only takes effect when
	// AllowSetVar is true (AllowSetVar=false already blocks every SET).
	BlockSetVars []string `toml:"block_set_vars"`

	// SignatureFilterEnabled and friends match against the statement
	// normalized by the real PostgreSQL parser (constants replaced with
	// $1, $2, ... placeholders - see pg_query.Normalize), NOT the
	// dialect-specific "(col = _)" style produced by pgproxy's previous,
	// CockroachDB-derived parser. Signatures configured before the C2 fix
	// (REVIEW.md) must be rewritten to match.
	SignatureFilterEnabled  bool `toml:"signature_filter_enabled"`
	SignatureAllowByDefault bool `toml:"signature_allow_by_default"`
	SignatureAuditMode      bool `toml:"signature_audit_mode"`

	// OnParseError controls what happens to a statement the parser cannot
	// parse at all: "block" (default), "allow", or "audit". Since the C2
	// fix this uses the real PostgreSQL grammar (github.com/pganalyze/
	// pg_query_go, a cgo binding of libpg_query - the actual PostgreSQL
	// parser), so this path should now be rare in practice: syntax that
	// PostgreSQL itself accepts, pgproxy now parses too. It still exists
	// for genuinely malformed input and for syntax newer than the
	// bundled libpg_query version. See the Filter doc comment for the
	// "allow"/"audit" bypass implications, unchanged from before.
	OnParseError string `toml:"on_parse_error"`

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

// QueryFilter applies FilterConfig rules to SQL statements. Safe for
// concurrent use, including concurrent calls to UpdateConfig (REVIEW.md M8):
// config is stored behind an atomic.Pointer rather than a plain field so a
// config swap is never observed as a torn/partial update by a Filter call
// running on another goroutine.
type QueryFilter struct {
	config atomic.Pointer[FilterConfig]
}

// NewQueryFilter creates a new QueryFilter with the given configuration.
func NewQueryFilter(config FilterConfig) *QueryFilter {
	WarnIfFilterConfigIsUnsafe(config, glog.Warningf)
	f := &QueryFilter{}
	f.config.Store(&config)
	return f
}

// UpdateConfig atomically replaces the filter's configuration, so an
// operator can change filter rules without restarting the process
// (REVIEW.md M8, e.g. on SIGHUP - see cli.run). Takes effect for every
// Filter call that starts after this returns; a call already in progress
// finishes with whichever config it already loaded.
func (f *QueryFilter) UpdateConfig(config FilterConfig) {
	WarnIfFilterConfigIsUnsafe(config, glog.Warningf)
	f.config.Store(&config)
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

// extractStatements returns every pg_query node under root whose concrete
// type is one this filter has a rule for (*pgquery.SelectStmt,
// *pgquery.InsertStmt, ...), found by a generic reflection walk over
// exported fields, slices, interfaces and the protobuf oneof wrapper
// (pgquery.Node.Node) - so it doesn't matter whether the node sits at the
// statement's own top level, inside a CTE (at any depth), a FROM/JOIN
// subquery, a scalar/EXISTS subquery, an INSERT...SELECT source, an
// UPDATE...SET expression, a CREATE TABLE AS source, or an EXPLAIN/PREPARE
// body: the walk finds it regardless of where it is embedded (see
// REVIEW.md C1). Walking generically, rather than hand-listing every
// statement/expression field that might hold a nested statement, also
// means a shape this doc comment doesn't anticipate still gets found.
//
// A pointer's identity is tracked in seen to avoid re-visiting shared nodes
// (harmless for an AST, which is a tree, but cheap insurance against
// accidental sharing) and to bound the walk.
func extractStatements(root *pgquery.Node) []any {
	var out []any
	seen := make(map[uintptr]bool)
	walkNodeTree(reflect.ValueOf(root), seen, &out)
	return out
}

// filteredStmtTypes are the concrete pg_query node types extractStatements
// collects - exactly the types Filter's switch below has a case for.
func isFilteredStmtType(v any) bool {
	switch v.(type) {
	case *pgquery.SelectStmt, *pgquery.InsertStmt, *pgquery.UpdateStmt, *pgquery.DeleteStmt,
		*pgquery.TruncateStmt, *pgquery.AlterRoleStmt, *pgquery.VariableSetStmt, *pgquery.ExecuteStmt:
		return true
	default:
		return false
	}
}

func walkNodeTree(v reflect.Value, seen map[uintptr]bool, out *[]any) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		walkNodeTree(v.Elem(), seen, out)
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		ptr := v.Pointer()
		if seen[ptr] {
			return
		}
		seen[ptr] = true
		if iface := v.Interface(); isFilteredStmtType(iface) {
			*out = append(*out, iface)
		}
		walkNodeTree(v.Elem(), seen, out)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported field (protobuf internals: state, sizeCache, ...)
			}
			walkNodeTree(v.Field(i), seen, out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkNodeTree(v.Index(i), seen, out)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walkNodeTree(v.MapIndex(k), seen, out)
		}
	}
}

// stmtText returns the exact source text of a single top-level statement
// out of the (possibly multi-statement) raw query string, using the
// location/length the parser reported for it. A reported length of 0 means
// "to the end of the string" (the convention libpg_query uses for the last
// statement).
func stmtText(query string, raw *pgquery.RawStmt) string {
	loc := int(raw.StmtLocation)
	if loc < 0 || loc > len(query) {
		return query
	}
	length := int(raw.StmtLen)
	if length <= 0 || loc+length > len(query) {
		return strings.TrimSpace(query[loc:])
	}
	return strings.TrimSpace(query[loc : loc+length])
}

// Filter checks if the SQL statement meets the configured criteria.
// Returns true if the query is safe and should be allowed.
func (f *QueryFilter) Filter(str []byte) bool {
	// Load once: a config swapped in mid-call (via UpdateConfig, e.g. on
	// SIGHUP - REVIEW.md M8) must not be applied inconsistently within a
	// single Filter call.
	cfg := *f.config.Load()

	query := string(str)
	result, err := pgquery.Parse(query)
	if err != nil {
		glog.Errorf("Parse error: %v", err)
		if cfg.OnParseError == "allow" || cfg.OnParseError == "audit" {
			if cfg.OnParseError == "audit" {
				// A query that failed to parse has no AST to Normalize, so
				// the raw text is logged - best-effort redact PASSWORD/
				// IDENTIFIED BY literals first (M9).
				glog.Warningf("AUDIT (Parse Error): %s", redactSecrets(query))
			}
			return true
		}
		return false
	}

	for _, raw := range result.Stmts {
		if cfg.SignatureFilterEnabled {
			sig, sigErr := pgquery.Normalize(stmtText(query, raw))
			if sigErr != nil {
				// Normalize can fail even though the full-query Parse above
				// succeeded (e.g. a utility statement Normalize doesn't
				// support). Fail closed on the signature check rather than
				// silently skipping it.
				glog.Errorf("Normalize error: %v", sigErr)
				return false
			}
			sig = strings.TrimSpace(sig)

			blocked := false
			for _, b := range cfg.BlockSignatures {
				if sig == b {
					blocked = true
					break
				}
			}

			if !blocked {
				allowed := false
				for _, a := range cfg.AllowSignatures {
					if sig == a {
						allowed = true
						break
					}
				}
				if !allowed && !cfg.SignatureAllowByDefault {
					blocked = true
				}
			}

			if blocked {
				if cfg.SignatureAuditMode {
					glog.Infof("SIGNATURE_AUDIT: %q", sig)
				} else {
					return false
				}
			}
		}

		for _, node := range extractStatements(raw.Stmt) {
			switch n := node.(type) {
			case *pgquery.SelectStmt:
				if !cfg.AllowSelect {
					return false
				}
			case *pgquery.DeleteStmt:
				if !cfg.AllowDelete {
					return false
				}
				if cfg.RequireWhereForDelete && n.WhereClause == nil {
					return false
				}
			case *pgquery.UpdateStmt:
				if !cfg.AllowUpdate {
					return false
				}
				if cfg.RequireWhereForUpdate && n.WhereClause == nil {
					return false
				}
			case *pgquery.InsertStmt:
				if !cfg.AllowInsert {
					return false
				}
			case *pgquery.TruncateStmt:
				if !cfg.AllowTruncate {
					return false
				}
			case *pgquery.AlterRoleStmt:
				if !cfg.AllowAlterRole {
					return false
				}
			case *pgquery.VariableSetStmt:
				if !cfg.AllowSetVar {
					return false
				}
				for _, blocked := range cfg.BlockSetVars {
					if strings.EqualFold(n.Name, blocked) {
						return false
					}
				}
			case *pgquery.ExecuteStmt:
				if !cfg.AllowExecute {
					return false
				}
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
