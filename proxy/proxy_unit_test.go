package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/jackc/pgproto3/v2"
	"testing"
)

// TestHandleQueryFunc tests the HandleQuery function with various inputs
func TestHandleQueryFunc(t *testing.T) {
	tests := []struct {
		name        string
		msgType     byte
		content     []byte
		handler     Handler
		wantContent []byte
		wantErr     bool
	}{
		{
			name:    "Simple SELECT query - passthrough",
			msgType: SimpleQuery,
			content: []byte("SELECT * FROM users;\x00"),
			handler: func(query string) ([]byte, error) {
				return []byte(query), nil
			},
			wantContent: []byte("SELECT * FROM users;\x00"),
			wantErr:     false,
		},
		{
			name:    "Simple SELECT query - modified",
			msgType: SimpleQuery,
			content: []byte("SELECT * FROM users;\x00"),
			handler: func(query string) ([]byte, error) {
				return []byte("SELECT * FROM orgs;"), nil
			},
			wantContent: []byte("SELECT * FROM orgs;\x00"),
			wantErr:     false,
		},
		{
			name:    "Query without null terminator",
			msgType: SimpleQuery,
			content: []byte("SELECT 1"),
			handler: func(query string) ([]byte, error) {
				return []byte(query), nil
			},
			wantContent: []byte("SELECT 1\x00"),
			wantErr:     false,
		},
		{
			name:    "Handler returns error",
			msgType: SimpleQuery,
			content: []byte("SELECT * FROM users"),
			handler: func(query string) ([]byte, error) {
				return nil, errors.New("handler error")
			},
			wantContent: nil,
			wantErr:     true,
		},
		{
			name:    "Handler returns nil data",
			msgType: SimpleQuery,
			content: []byte("SELECT 1\x00"),
			handler: func(query string) ([]byte, error) {
				return nil, nil
			},
			wantContent: []byte("SELECT 1\x00"),
			wantErr:     false,
		},
		{
			name:    "Empty content",
			msgType: SimpleQuery,
			content: []byte(""),
			handler: func(query string) ([]byte, error) {
				return []byte("SELECT 1"), nil
			},
			wantContent: []byte("SELECT 1\x00"),
			wantErr:     false,
		},
		{
			name:    "Parse message - passthrough",
			msgType: ParseMsg,
			content: []byte("my_query\x00"),
			handler: func(query string) ([]byte, error) {
				return []byte(query), nil
			},
			wantContent: []byte("my_query\x00"),
			wantErr:     false,
		},
		{
			name:    "Unknown message type - passthrough",
			msgType: 'X', // Terminate
			content: []byte("some content\x00"),
			handler: func(query string) ([]byte, error) {
				return []byte(query), nil
			},
			wantContent: []byte("some content\x00"),
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HandleQuery(tt.msgType, tt.content, tt.handler)

			if (err != nil) != tt.wantErr {
				t.Errorf("HandleQuery() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !bytes.Equal(got, tt.wantContent) {
				t.Errorf("HandleQuery() = %v, want %v", got, tt.wantContent)
			}
		})
	}
}

// TestBufferPool tests the buffer pool functionality
func TestBufferPool(t *testing.T) {
	pool := &bufferPool{}

	// Get a buffer
	buf1 := pool.Get()
	if len(buf1) != 65536 {
		t.Errorf("Expected buffer length 65536, got %d", len(buf1))
	}

	// Put it back
	pool.Put(buf1)

	// Get another buffer (should reuse)
	buf2 := pool.Get()
	if len(buf2) != 65536 {
		t.Errorf("Expected buffer length 65536, got %d", len(buf2))
	}

	// Test with small buffer (should not be pooled)
	smallBuf := make([]byte, 100)
	pool.Put(smallBuf)

	// Get should still return 65536 buffer
	buf3 := pool.Get()
	if len(buf3) != 65536 {
		t.Errorf("Expected buffer length 65536 after small put, got %d", len(buf3))
	}
}

// TestMessageParsing tests PostgreSQL message format parsing
func TestMessageParsing(t *testing.T) {
	// Test creating a simple query message
	query := "SELECT * FROM users;"
	qMsg := &pgproto3.Query{String: query}
	msg := qMsg.Encode(nil)

	// Verify it encodes correctly
	if msg[0] != 'Q' {
		t.Errorf("Expected 'Q' prefix")
	}
	// Extract query string
	backend := pgproto3.NewBackend(pgproto3.NewChunkReader(bytes.NewReader(msg)), nil)
	decoded, _ := backend.Receive()
	if q, ok := decoded.(*pgproto3.Query); !ok || q.String != query {
		t.Errorf("Query string mismatch")
	}
}

// TestIsQueryMessage tests the isQueryMessage function
func TestIsQueryMessage(t *testing.T) {
	tests := []struct {
		msgType byte
		want    bool
	}{
		{'Q', true},  // SimpleQuery
		{'P', true},  // ParseMsg
		{'B', true},  // BindMsg
		{'E', false}, // ExecuteMsg
		{'D', false}, // DescribeMsg
		{'C', false}, // CloseMsg
		{'S', false}, // SyncMsg
		{'X', false}, // Terminate
		{'R', false}, // Unknown
	}

	for _, tt := range tests {
		t.Run(string(tt.msgType), func(t *testing.T) {
			got := isQueryMessage(tt.msgType)
			if got != tt.want {
				t.Errorf("isQueryMessage(%c) = %v, want %v", tt.msgType, got, tt.want)
			}
		})
	}
}

func TestBuildErrorResponse(t *testing.T) {
	resp := buildErrorResponse("FATAL", "test error message")

	// The response should be a valid pgproto3.ErrorResponse message
	// It should start with 'E'
	if len(resp) < 5 || resp[0] != 'E' {
		t.Fatalf("Expected response to start with 'E', got %v", resp)
	}

	// Check the length (bytes 1-4)
	length := binary.BigEndian.Uint32(resp[1:5])
	if int(length) != len(resp)-1 {
		t.Errorf("Expected length %d, got %d", len(resp)-1, length)
	}

	// Verify it contains the severity and message
	if !bytes.Contains(resp, []byte("FATAL")) {
		t.Errorf("Expected response to contain 'FATAL'")
	}
	if !bytes.Contains(resp, []byte("test error message")) {
		t.Errorf("Expected response to contain 'test error message'")
	}
}
