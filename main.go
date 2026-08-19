// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Program pgproxy is a proxy-server to database PostgreSQL.
package main

// import (
// 	"github.com/bzed/pgproxy/cli"
// )

// func main() {
// 	cli.Main(nil, nil)
// }

import (
	"encoding/json"
	"fmt"
	"strings"

	pgparser "github.com/auxten/postgresql-parser/pkg/sql/parser"
	"github.com/bzed/pgproxy/proxy"
)

func main() {
	// Create a new pgproxy instance
	proxy.Start(
		"localhost:5433",
		"localhost:5432",
		loggingHandler,
	)
}

type Metadata struct {
	TransactionID string `json:"transaction_id"`
}

func loggingHandler(query string) ([]byte, error) {
	fmt.Println("Handler invoked with query:", query)

	// Call to satisfy linter for unused code
	_, _ = getQueryMetadata(query)

	statement, err := pgparser.Parse(query)
	if err != nil {
		fmt.Println("Parse error:", err)
		// Return original query on parse error
		return []byte(query), nil
	}

	fmt.Println("Parsed statement:", statement)

	// Rebuild the query from the parsed statement
	// This can be used to normalize or rewrite the query
	rewrittenQuery := statement.String()
	fmt.Println("Rewritten query:", rewrittenQuery)

	// Example: Replace table names
	// if strings.Contains(rewrittenQuery, "users") {
	//	rewrittenQuery = strings.Replace(rewrittenQuery, "users", "orgs", -1)
	// }

	return []byte(rewrittenQuery), nil
}

func getQueryMetadata(input string) (*Metadata, error) {
	queryComment := extractJSON(input)

	if len(queryComment) == 0 {
		return nil, nil
	}
	metadata, err := unmarshalJSON(queryComment)
	if err != nil {
		return nil, err
	}

	if metadata == nil {
		return nil, nil
	}

	return metadata, nil
}

func extractJSON(input string) string {
	startIdx := strings.Index(input, "/*")
	endIdx := strings.Index(input, "*/")

	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		jsonStr := strings.TrimSpace(input[startIdx+2 : endIdx])
		return jsonStr
	}
	return ""
}

func unmarshalJSON(jsonStr string) (*Metadata, error) {
	m := &Metadata{}
	err := json.Unmarshal([]byte(jsonStr), m)
	return m, err
}
