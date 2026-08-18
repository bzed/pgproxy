package parser

import (
	"testing"
)

func TestAnalyzerExtra(t *testing.T) {
	if !StringIn("a", "b", "a") {
		t.Errorf("StringIn failed")
	}

	if StringIn("c", "b", "a") {
		t.Errorf("StringIn should be false")
	}

	if !IsValue(StrVal([]byte("abc"))) {
		t.Errorf("IsValue StrVal failed")
	}
	if !IsValue(NumVal([]byte("123"))) {
		t.Errorf("IsValue NumVal failed")
	}

	if !IsColName(&ColName{}) {
		t.Errorf("IsColName failed")
	}

	colName := GetColName(&ColName{Name: []byte("test")})
	if colName != "test" {
		t.Errorf("GetColName failed")
	}

	tableName := GetTableName(&TableName{Name: []byte("test")})
	if tableName != "test" {
		t.Errorf("GetTableName failed")
	}

	// HasINClause
	hasIn := HasINClause([]BoolExpr{&ComparisonExpr{Operator: AST_IN}})
	if !hasIn {
		t.Errorf("HasINClause failed")
	}

	_, err := AsInterface(StrVal([]byte("abc")))
	if err != nil {
		t.Errorf("AsInterface StrVal failed")
	}

	_, err = AsInterface(NumVal([]byte("123")))
	if err != nil {
		t.Errorf("AsInterface NumVal failed")
	}
}
