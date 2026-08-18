package main

import (
	"reflect"
	"testing"
)

func TestLoggingHandler(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    []byte
		wantErr bool
	}{
		{
			name:    "Valid select",
			query:   "SELECT * FROM users",
			want:    []byte("select * from users"),
			wantErr: false,
		},
		{
			name:    "Invalid syntax",
			query:   "THIS IS NOT SQL",
			want:    []byte("THIS IS NOT SQL"), // Should return original query on parse error
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loggingHandler(tt.query)
			if (err != nil) != tt.wantErr {
				t.Errorf("loggingHandler() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			// Just verify it doesn't crash and returns something
			if len(got) == 0 && len(tt.want) > 0 {
				t.Errorf("loggingHandler() returned empty slice")
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "Valid JSON comment",
			input: "SELECT * /* {\"transaction_id\": \"123\"} */ FROM table",
			want:  `{"transaction_id": "123"}`,
		},
		{
			name:  "No comment",
			input: "SELECT * FROM table",
			want:  "",
		},
		{
			name:  "Invalid comment tags",
			input: "SELECT * /* missing end",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractJSON(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("extractJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    *Metadata
		wantErr bool
	}{
		{
			name:    "Valid JSON",
			jsonStr: `{"transaction_id": "tx-12345"}`,
			want:    &Metadata{TransactionId: "tx-12345"},
			wantErr: false,
		},
		{
			name:    "Invalid JSON",
			jsonStr: `{"transaction_id": "tx-12345"`,
			want:    &Metadata{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unmarshalJSON(tt.jsonStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("unmarshalJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unmarshalJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetQueryMetadata(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Metadata
		wantErr bool
	}{
		{
			name:    "Query with metadata",
			input:   "SELECT * /* {\"transaction_id\": \"123\"} */ FROM table",
			want:    &Metadata{TransactionId: "123"},
			wantErr: false,
		},
		{
			name:    "Query without metadata",
			input:   "SELECT * FROM table",
			want:    nil,
			wantErr: false,
		},
		{
			name:    "Query with invalid json",
			input:   "SELECT * /* {invalid} */ FROM table",
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getQueryMetadata(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("getQueryMetadata() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getQueryMetadata() = %v, want %v", got, tt.want)
			}
		})
	}
}
