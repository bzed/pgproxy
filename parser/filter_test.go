package parser

import (
	"testing"
)

func TestQueryFilter(t *testing.T) {
	config := DefaultFilterConfig()
	filter := NewQueryFilter(config)

	if !filter.Filter([]byte("select a from b")) {
		t.Errorf("Filter valid select failed")
	}

	if !filter.Filter([]byte("select * from b")) {
		t.Errorf("Filter select * should be allowed now by default")
	}

	if !filter.Filter([]byte("delete from a where id = 1")) {
		t.Errorf("Filter delete with where failed")
	}

	if filter.Filter([]byte("delete from a")) {
		t.Errorf("Filter unbounded delete should return false")
	}

	if filter.Filter([]byte("truncate table a")) {
		t.Errorf("Filter truncate should return false")
	}

	if !filter.Filter([]byte("insert into a(id) values(1)")) {
		t.Errorf("Filter insert failed")
	}

	if !filter.Filter([]byte("update a set b=1 where id = 1")) {
		t.Errorf("Filter bounded update failed")
	}

	if filter.Filter([]byte("update a set b=1")) {
		t.Errorf("Filter unbounded update should return false")
	}

	if filter.Filter([]byte("select * from")) {
		t.Errorf("Filter invalid syntax should fail")
	}
}

func TestQueryFilterConfig(t *testing.T) {
	config := DefaultFilterConfig()
	config.AllowSelect = false
	config.RequireWhereForDelete = false

	filter := NewQueryFilter(config)

	if filter.Filter([]byte("select * from a")) {
		t.Errorf("Filter should block select when AllowSelect is false")
	}

	if !filter.Filter([]byte("delete from a")) {
		t.Errorf("Filter should allow unbounded delete when RequireWhereForDelete is false")
	}
}

func TestQueryFilterSignatures(t *testing.T) {
	config := DefaultFilterConfig()
	config.BlockSignatures = []string{
		"SELECT * FROM users WHERE (id = _) AND (name = _)",
	}

	filter := NewQueryFilter(config)

	// Blocked by signature
	if filter.Filter([]byte("SELECT * FROM users WHERE id = 123 AND name = 'alice'")) {
		t.Errorf("Filter should block query matching BlockSignatures")
	}
	if filter.Filter([]byte("SELECT * FROM users WHERE id = 456 AND name = 'bob'")) {
		t.Errorf("Filter should block query matching BlockSignatures regardless of literals")
	}

	// Allowed because signature differs
	if !filter.Filter([]byte("SELECT * FROM users WHERE id = 123")) {
		t.Errorf("Filter should allow queries with different signatures")
	}

	// Test AllowSignatures strictly
	config2 := DefaultFilterConfig()
	config2.AllowSignatures = []string{
		"SELECT id FROM allowed_table",
	}
	filter2 := NewQueryFilter(config2)

	if !filter2.Filter([]byte("SELECT id FROM allowed_table")) {
		t.Errorf("Filter should allow query in AllowSignatures")
	}
	if filter2.Filter([]byte("SELECT id, name FROM allowed_table")) {
		t.Errorf("Filter should block query NOT in AllowSignatures when AllowSignatures is populated")
	}
}
