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
