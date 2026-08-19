package parser

import (
	"testing"
)

func TestGetQueryModificada(t *testing.T) {
	// Should pass through
	out, err := GetQueryModificada("select * from abc")
	if string(out) != "select * from abc" || err != nil {
		t.Errorf("GetQueryModificada failed")
	}

	// Should rewrite power
	out, err = GetQueryModificada("power select")
	if string(out) != "select * from clientes limit 1;" || err != nil {
		t.Errorf("GetQueryModificada power failed")
	}
}

func TestReturnHandler(t *testing.T) {
	out, err := ReturnHandler("select 1")
	if string(out) != "select 1" || err != nil {
		t.Errorf("ReturnHandler failed")
	}
}

func TestFilter(t *testing.T) {
	// Filter calls Parse
	if !Filter([]byte("select a from b")) {
		t.Errorf("Filter valid select failed")
	}

	if Filter([]byte("select * from b")) {
		t.Errorf("Filter select * should return false")
	}

	if !Filter([]byte("delete from a where id = 1")) {
		t.Errorf("Filter delete with where failed")
	}

	if Filter([]byte("delete from a")) {
		t.Errorf("Filter unbounded delete should return false")
	}

	if !Filter([]byte("insert into a(id) values(1)")) {
		t.Errorf("Filter insert failed")
	}

	if !Filter([]byte("update a set b=1 where id = 1")) {
		t.Errorf("Filter bounded update failed")
	}

	if Filter([]byte("update a set b=1")) {
		t.Errorf("Filter unbounded update should return false")
	}

	if Filter([]byte("select * from")) {
		t.Errorf("Filter invalid syntax should fail")
	}
}
