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
	RequireWhereForUpdate bool `toml:"require_where_for_update"`
	RequireWhereForDelete bool `toml:"require_where_for_delete"`
}

// DefaultFilterConfig returns a secure default configuration.
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		AllowSelect:           true,
		AllowInsert:           true,
		AllowUpdate:           true,
		AllowDelete:           true,
		AllowTruncate:         false,
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
	return &QueryFilter{config: config}
}

// Filter checks if the SQL statement meets the configured criteria.
// Returns true if the query is safe and should be allowed.
func (f *QueryFilter) Filter(str []byte) bool {
	stmts, err := pgparser.Parse(string(str))
	if err != nil {
		glog.Errorln(err)
		return false
	}

	for _, stmt := range stmts {
		switch ast := stmt.AST.(type) {
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
