// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package parser provides filtering rules if you need.
package parser

import (
	"fmt"
	"strings"

	pgparser "github.com/auxten/postgresql-parser/pkg/sql/parser"
	"github.com/auxten/postgresql-parser/pkg/sql/sem/tree"
	"github.com/golang/glog"
)

// GetQueryModificada callback - Handler function for proxy
// Receives query string and returns modified query bytes
func GetQueryModificada(query string) ([]byte, error) {
	// Example: if query doesn't start with "power", pass through
	if len(query) < 5 || query[:5] != "power" {
		return []byte(query), nil
	}
	return []byte("select * from clientes limit 1;"), nil
}

// Filter checks if the SQL statement meets certain criteria.
// Returns true if the query is safe and should be allowed.
func Filter(str []byte) bool {
	stmts, err := pgparser.Parse(string(str))
	if err != nil {
		glog.Errorln(err)
		return false
	}

	for _, stmt := range stmts {
		switch ast := stmt.AST.(type) {
		case *tree.Select:
			if !isSafeSelect(ast) {
				return false
			}
		case *tree.Delete:
			if !isSafeDelete(ast) {
				return false
			}
		case *tree.Update:
			if !isSafeUpdate(ast) {
				return false
			}
		case *tree.Insert:
			// Inserts are generally safe from unbounded mutation
			continue
		case *tree.Truncate:
			// Block TRUNCATE (equivalent to unbounded DELETE)
			return false
		}
	}
	return true
}

// ReturnHandler is a Handler that just logs and passes through the query
func ReturnHandler(query string) ([]byte, error) {
	fmt.Println("Query:", query)
	return []byte(query), nil
}

// isSafeSelect prevents SELECT * and ORDER BY random() queries.
func isSafeSelect(ast *tree.Select) bool {
	if ast.OrderBy != nil {
		if strings.Contains(strings.ToLower(tree.AsString(&ast.OrderBy)), "rand()") ||
			strings.Contains(strings.ToLower(tree.AsString(&ast.OrderBy)), "random()") {
			return false
		}
	}

	if sc, ok := ast.Select.(*tree.SelectClause); ok {
		if tree.AsString(&sc.Exprs) == "*" {
			return false
		}
	}
	return true
}

// isSafeDelete ensures we don't execute unbounded deletes.
func isSafeDelete(ast *tree.Delete) bool {
	// Must have a WHERE clause to prevent deleting all rows
	return ast.Where != nil
}

// isSafeUpdate ensures we don't execute unbounded updates.
func isSafeUpdate(ast *tree.Update) bool {
	// Must have a WHERE clause to prevent updating all rows
	return ast.Where != nil
}
