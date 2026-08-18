package proxy

import (
	"fmt"
	"testing"
)

type mockResult struct {
	affected int64
	err      error
}

func (m mockResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (m mockResult) RowsAffected() (int64, error) {
	return m.affected, m.err
}

func TestResultFormater(t *testing.T) {
	// Should output to stdout, which is fine
	ResultFormater(mockResult{affected: 5, err: nil})

	// Test error case
	ResultFormater(mockResult{affected: 0, err: fmt.Errorf("mock error")})
}

func TestRowsFormaterNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected panic when passing nil")
		}
	}()
	RowsFormater(nil)
}

func TestInterface2String(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected string
	}{
		{input: "hello", expected: "hello"},
		{input: int64(123), expected: "123"},
		{input: []byte("world"), expected: "world"},
		{input: 123, expected: ""}, // fallback
		{input: nil, expected: ""},
		{input: 1.5, expected: ""},
	}

	for _, test := range tests {
		actual := interface2String(test.input)
		if actual != test.expected {
			t.Errorf("interface2String(%v) = %v, expected %v", test.input, actual, test.expected)
		}
	}
}
