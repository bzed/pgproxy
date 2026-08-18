// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package parser provides filtering rules if you need.
package parser

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
)

// GetQueryModificada callback - Handler function for proxy
// Receives query string and returns modified query bytes
func GetQueryModificada(query string) ([]byte, error) {
	// Example: if query doesn't start with "power", pass through
	// Note: this is a simple example, real implementations would do more sophisticated filtering
	if len(query) < 5 || query[:5] != "power" {
		return []byte(query), nil
	}
	return []byte("select * from clientes limit 1;"), nil
}

// Filter checks if the SQL statement meets certain criteria
// Returns true if the query should be allowed
func Filter(str []byte) bool {
	tree, err := Parse(string(str))
	if err != nil {
		glog.Errorln(err)
		return false
	}

	switch tree.(type) {
	case *Select:
		return ParseSelect(tree.(*Select))
	case *Delete:
		return ParseDelete(tree.(*Delete))
	case *Insert:
		return ParseInsert(tree.(*Insert))
	case *Update:
		return ParseUpdate(tree.(*Update))
	}
	return false
}

// ReturnHandler is a Handler that just logs and passes through the query
func ReturnHandler(query string) ([]byte, error) {
	fmt.Println("Query:", query)
	return []byte(query), nil
}

func ParseSelect(sql *Select) bool {
	return !Is_SELECT_ALL(sql) && !Is_ORDER_BY_RAND(sql)
}

func Is_SELECT_ALL(sql *Select) bool {
	buf := NewTrackedBuffer(nil)
	sql.SelectExprs.Format(buf)
	if "*" == buf.String() {
		return true
	}
	return false
}

func Is_ORDER_BY_RAND(sql *Select) bool {
	buf := NewTrackedBuffer(nil)
	sql.OrderBy.Format(buf)
	if "rand()" == strings.ToLower(buf.String()) {
		return true
	}
	return false
}

func ParseDelete(sql *Delete) bool {
	return !Is_BIG_DELETE(sql)
}

func Is_BIG_DELETE(sql *Delete) bool {
	buf := NewTrackedBuffer(nil)
	sql.Limit.Format(buf)
	if "1000" < buf.String() {
		return true
	}
	return false
}

func ParseInsert(sql *Insert) bool {
	return !Is_BIG_INSERT(sql)
}

func Is_BIG_INSERT(sql *Insert) bool {
	buf := NewTrackedBuffer(nil)
	sql.Rows.Format(buf)
	if "1000" < buf.String() {
		return true
	}
	return false
}

func ParseUpdate(sql *Update) bool {
	return true
}
