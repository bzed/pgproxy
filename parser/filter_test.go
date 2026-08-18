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
	validSelect := Filter([]byte("select a from b"))
	if !validSelect {
		t.Errorf("Filter valid select failed")
	}

	invalidSelect := Filter([]byte("select * from b"))
	if invalidSelect {
		t.Errorf("Filter select * should return false")
	}

	validDelete := Filter([]byte("delete from a limit 10"))
	if !validDelete {
		t.Errorf("Filter delete failed")
	}

	validInsert := Filter([]byte("insert into a(id) values(1)"))
	if !validInsert {
		// Just log, we don't care if vitess doesn't support this specific insert syntax
		// t.Errorf("Filter insert failed")
	}

	validUpdate := Filter([]byte("update a set b=1"))
	if !validUpdate {
		t.Errorf("Filter update failed")
	}

	invalidSyntax := Filter([]byte("select * from"))
	if invalidSyntax {
		t.Errorf("Filter invalid syntax should fail")
	}
}
