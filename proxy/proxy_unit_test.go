package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
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
	msgType := byte('Q')

	// Create message content with null terminator
	content := append([]byte(query), 0)

	// Message length = content length + 4 (for the length field itself)
	msgLength := uint32(len(content) + 4)

	// Create full message
	msg := make([]byte, 5+len(content))
	msg[0] = msgType
	binary.BigEndian.PutUint32(msg[1:5], msgLength)
	copy(msg[5:], content)

	// Verify we can parse it back
	parsedMsgType := msg[0]
	parsedLength := binary.BigEndian.Uint32(msg[1:5])
	parsedContent := msg[5:]

	if parsedMsgType != msgType {
		t.Errorf("Message type mismatch: got %c, want %c", parsedMsgType, msgType)
	}

	if parsedLength != msgLength {
		t.Errorf("Message length mismatch: got %d, want %d", parsedLength, msgLength)
	}

	// Content should end with null terminator
	if len(parsedContent) == 0 || parsedContent[len(parsedContent)-1] != 0 {
		t.Errorf("Content should end with null terminator")
	}

	// Extract query string (without null terminator)
	queryStr := string(bytes.TrimSuffix(parsedContent, []byte{0}))
	if queryStr != query {
		t.Errorf("Query string mismatch: got %q, want %q", queryStr, query)
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
