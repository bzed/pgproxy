package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/jackc/pgproto3/v2"
)

// TestApplyFrontendHandler tests applyFrontendHandler with various message
// types, in particular that Bind messages (and their binary parameters) are
// forwarded byte-for-byte instead of being run through the SQL handler.
func TestApplyFrontendHandler(t *testing.T) {
	p := &Proxy{}

	t.Run("Query - passthrough", func(t *testing.T) {
		handler := func(query string) ([]byte, error) { return []byte(query), nil }
		msg := &pgproto3.Query{String: "SELECT * FROM users;"}
		got, err := p.applyFrontendHandler(msg, handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := (&pgproto3.Query{String: "SELECT * FROM users;"}).Encode(nil)
		if !bytes.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("Query - rewritten", func(t *testing.T) {
		handler := func(query string) ([]byte, error) { return []byte("SELECT * FROM orgs;"), nil }
		msg := &pgproto3.Query{String: "SELECT * FROM users;"}
		got, err := p.applyFrontendHandler(msg, handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := (&pgproto3.Query{String: "SELECT * FROM orgs;"}).Encode(nil)
		if !bytes.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("Query - handler nil result means passthrough", func(t *testing.T) {
		handler := func(query string) ([]byte, error) { return nil, nil }
		msg := &pgproto3.Query{String: "SELECT 1"}
		got, err := p.applyFrontendHandler(msg, handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := (&pgproto3.Query{String: "SELECT 1"}).Encode(nil)
		if !bytes.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("Query - handler error blocks", func(t *testing.T) {
		handler := func(query string) ([]byte, error) { return nil, errors.New("blocked") }
		msg := &pgproto3.Query{String: "DELETE FROM users"}
		if _, err := p.applyFrontendHandler(msg, handler); err == nil {
			t.Error("expected an error, got nil")
		}
	})

	t.Run("Parse - query text is filtered, name and OIDs preserved", func(t *testing.T) {
		handler := func(query string) ([]byte, error) { return []byte("SELECT * FROM orgs"), nil }
		msg := &pgproto3.Parse{Name: "stmt1", Query: "SELECT * FROM users", ParameterOIDs: []uint32{23, 25}}
		got, err := p.applyFrontendHandler(msg, handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := (&pgproto3.Parse{Name: "stmt1", Query: "SELECT * FROM orgs", ParameterOIDs: []uint32{23, 25}}).Encode(nil)
		if !bytes.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("Bind - forwarded verbatim, binary parameters untouched", func(t *testing.T) {
		// A handler that would corrupt anything it's actually given as a
		// "query" string, to prove Bind's binary payload never reaches it.
		handler := func(query string) ([]byte, error) { return []byte("CORRUPTED"), nil }
		msg := &pgproto3.Bind{
			DestinationPortal:    "",
			PreparedStatement:    "stmt1",
			ParameterFormatCodes: []int16{1},
			Parameters:           [][]byte{{0x00, 0x00, 0x00, 0x2a}}, // binary int4 = 42
			ResultFormatCodes:    []int16{1},
		}
		got, err := p.applyFrontendHandler(msg, handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := msg.Encode(nil)
		if !bytes.Equal(got, want) {
			t.Errorf("Bind message was altered:\n got  %v\n want %v", got, want)
		}
	})

	t.Run("Terminate - forwarded verbatim", func(t *testing.T) {
		msg := &pgproto3.Terminate{}
		got, err := p.applyFrontendHandler(msg, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(got, msg.Encode(nil)) {
			t.Errorf("Terminate message was altered")
		}
	})
}

func TestRunHandler(t *testing.T) {
	if s, err := runHandler(nil, "SELECT 1"); err != nil || s != "SELECT 1" {
		t.Errorf("nil handler should passthrough, got (%q, %v)", s, err)
	}

	nilHandler := func(query string) ([]byte, error) { return nil, nil }
	if s, err := runHandler(nilHandler, "SELECT 1"); err != nil || s != "SELECT 1" {
		t.Errorf("handler returning nil should passthrough, got (%q, %v)", s, err)
	}

	errHandler := func(query string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := runHandler(errHandler, "SELECT 1"); err == nil {
		t.Error("expected error to propagate")
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
