// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package parser provides filtering rules if you need.
package parser

import (
	"fmt"

	pgparser "github.com/auxten/postgresql-parser/pkg/sql/parser"
	"github.com/auxten/postgresql-parser/pkg/sql/sem/tree"
	"github.com/golang/glog"
)

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
		AllowSelect:           true,
		AllowInsert:           true,
		AllowUpdate:           true,
		AllowDelete:           true,
		AllowTruncate:         false,
		AllowAlterRole:        false,
		AllowSetVar:           false,
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
// intentional "block everything" firewall, and every blocked query kills
// the client connection (see the Handler doc comment).
func WarnIfFilterConfigIsUnsafe(config FilterConfig, warnf func(format string, args ...interface{})) {
	if config.SignatureFilterEnabled && !config.SignatureAllowByDefault && len(config.AllowSignatures) == 0 {
		warnf("Filter config: signature_filter_enabled=true, signature_allow_by_default=false and " +
			"allow_signatures is empty - EVERY statement will be blocked (and, per the current Handler " +
			"behavior, every client connection killed). Add entries to allow_signatures, set " +
			"signature_allow_by_default=true, or set signature_filter_enabled=false.")
	}
}

// Filter checks if the SQL statement meets the configured criteria.
// Returns true if the query is safe and should be allowed.

func extractStatements(stmt tree.Statement, stmts *[]tree.Statement) {
	*stmts = append(*stmts, stmt)
	switch s := stmt.(type) {
	case *tree.Explain:
		if s.Statement != nil {
			extractStatements(s.Statement, stmts)
		}
	case *tree.Prepare:
		if s.Statement != nil {
			extractStatements(s.Statement, stmts)
		}
	case *tree.Select:
		if s.With != nil {
			for _, cte := range s.With.CTEList {
				extractStatements(cte.Stmt, stmts)
			}
		}
	case *tree.Insert:
		if s.With != nil {
			for _, cte := range s.With.CTEList {
				extractStatements(cte.Stmt, stmts)
			}
		}
	case *tree.Update:
		if s.With != nil {
			for _, cte := range s.With.CTEList {
				extractStatements(cte.Stmt, stmts)
			}
		}
	case *tree.Delete:
		if s.With != nil {
			for _, cte := range s.With.CTEList {
				extractStatements(cte.Stmt, stmts)
			}
		}
	}
}

func (f *QueryFilter) Filter(str []byte) bool {
	stmts, err := pgparser.Parse(string(str))
	if err != nil {
		glog.Errorf("Parse error: %v", err)
		if f.config.OnParseError == "allow" || f.config.OnParseError == "audit" {
			if f.config.OnParseError == "audit" {
				glog.Warningf("AUDIT (Parse Error): %s", string(str))
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

		var allStmts []tree.Statement
		extractStatements(stmt.AST, &allStmts)

		for _, ast := range allStmts {
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
