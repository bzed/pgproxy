package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
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
		want, _ := (&pgproto3.Query{String: "SELECT * FROM users;"}).Encode(nil)
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
		want, _ := (&pgproto3.Query{String: "SELECT * FROM orgs;"}).Encode(nil)
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
		want, _ := (&pgproto3.Query{String: "SELECT 1"}).Encode(nil)
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
		want, _ := (&pgproto3.Parse{Name: "stmt1", Query: "SELECT * FROM orgs", ParameterOIDs: []uint32{23, 25}}).Encode(nil)
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
		want := encodeMsg(msg)
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
		if !bytes.Equal(got, encodeMsg(msg)) {
			t.Errorf("Terminate message was altered")
		}
	})

	t.Run("Parse - handler error blocks", func(t *testing.T) {
		handler := func(query string) ([]byte, error) { return nil, errors.New("blocked") }
		msg := &pgproto3.Parse{Name: "stmt1", Query: "DELETE FROM users"}
		if _, err := p.applyFrontendHandler(msg, handler); err == nil {
			t.Error("expected an error, got nil")
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
	msg, _ := qMsg.Encode(nil)

	// Verify it encodes correctly
	if msg[0] != 'Q' {
		t.Errorf("Expected 'Q' prefix")
	}
	// Extract query string
	backend := pgproto3.NewBackend(bytes.NewReader(msg), nil)
	decoded, _ := backend.Receive()
	if q, ok := decoded.(*pgproto3.Query); !ok || q.String != query {
		t.Errorf("Query string mismatch")
	}
}

func TestBuildErrorResponse(t *testing.T) {
	resp := buildErrorResponse("FATAL", "test error message", "XX000")

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

// TestBuildErrorResponse_DefaultCode covers the empty-code fallback to the
// generic SQLSTATE "XX000".
func TestBuildErrorResponse_DefaultCode(t *testing.T) {
	resp := buildErrorResponse("ERROR", "boom", "")
	if !bytes.Contains(resp, []byte("XX000")) {
		t.Errorf("expected the default SQLSTATE XX000 in the response, got %v", resp)
	}
}

// TestProxyErr_NilError covers the err(msg, nil) logging branch (used for
// application-level failures that aren't an *error, e.g. "database not
// configured").
func TestProxyErr_NilError(t *testing.T) {
	p := &Proxy{errsig: make(chan struct{}), prefix: "test "}

	done := make(chan struct{})
	go func() {
		<-p.errsig
		close(done)
	}()

	p.err("something happened", nil)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("errsig was not closed")
	}
}

// TestForwardCancelRequest_UnknownAndUnreachable covers forwardCancelRequest's
// early-return branches: a PID/secret not in cancelRegistry, and a
// registered backend that can no longer be dialed. Both should log and
// return without panicking; the full happy path (a real backend receiving
// the forwarded request) is covered by TestProxyForwardsCancelRequest.
func TestForwardCancelRequest_UnknownAndUnreachable(t *testing.T) {
	t.Run("unknown PID/secret", func(t *testing.T) {
		p := &Proxy{prefix: "test "}
		cr := &pgproto3.CancelRequest{ProcessID: 999999, SecretKey: []byte{1, 2, 3, 4}}
		p.forwardCancelRequest(cr) // must not panic
	})

	t.Run("registered backend is no longer reachable", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to listen: %v", err)
		}
		addr := ln.Addr().String()
		ln.Close() // nothing listens here anymore

		key := cancelKey{pid: 777, secret: [4]byte{9, 9, 9, 9}}
		cancelRegistry.Store(key, backendSession{target: backendTarget{network: "tcp", addr: addr}, secretKey: key.secret})
		defer cancelRegistry.Delete(key)

		p := &Proxy{prefix: "test "}
		cr := &pgproto3.CancelRequest{ProcessID: 777, SecretKey: []byte{9, 9, 9, 9}}
		p.forwardCancelRequest(cr) // must not panic despite the dial failure
	})
}

// TestServiceStartup_DatabaseNotFound covers the "database not found in
// proxy config" branch: the client should get a FATAL ErrorResponse and
// serviceStartup should return.
func TestServiceStartup_DatabaseNotFound(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	p := &Proxy{lconn: server, errsig: make(chan struct{}), prefix: "test "}
	sm := &pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: map[string]string{"database": "nope"}}

	done := make(chan struct{})
	go func() {
		p.serviceStartup(sm, map[string]DBConfig{}, nil)
		close(done)
	}()

	frontend := pgproto3.NewFrontend(client, client)
	msg, err := frontend.Receive()
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if _, ok := msg.(*pgproto3.ErrorResponse); !ok {
		t.Fatalf("got %T, want *pgproto3.ErrorResponse", msg)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serviceStartup did not return")
	}
}

// TestServiceStartup_BackendConnectFailed covers the "backend connection
// failed" branch: the client should get a FATAL ErrorResponse when the
// configured backend can't be dialed.
func TestServiceStartup_BackendConnectFailed(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here anymore

	p := &Proxy{lconn: server, errsig: make(chan struct{}), prefix: "test "}
	dbs := map[string]DBConfig{"testdb": {Addr: addr, DBName: "testdb"}}
	sm := &pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: map[string]string{"database": "testdb"}}

	done := make(chan struct{})
	go func() {
		p.serviceStartup(sm, dbs, nil)
		close(done)
	}()

	frontend := pgproto3.NewFrontend(client, client)
	msg, err := frontend.Receive()
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if _, ok := msg.(*pgproto3.ErrorResponse); !ok {
		t.Fatalf("got %T, want *pgproto3.ErrorResponse", msg)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serviceStartup did not return")
	}
}

// TestHandleIncomingConnection_CleanEOF exercises handleIncomingConnection
// in isolation with a client that hangs up with no data in flight. Note
// this still surfaces as io.ErrUnexpectedEOF, not io.EOF:
// pgproto3.Backend.Receive() unconditionally translates a header read of 0
// bytes into ErrUnexpectedEOF (see translateEOFtoErrUnexpectedEOF in
// jackc/pgx/v5/pgproto3), so the `err == io.EOF` branch in handleIncoming/
// ResponseConnection is effectively unreachable through the pgproto3 API.
func TestHandleIncomingConnection_CleanEOF(t *testing.T) {
	client, lconn := net.Pipe()
	rconn, rconnPeer := net.Pipe()
	defer rconnPeer.Close()

	p := &Proxy{lconn: lconn, rconn: rconn, errsig: make(chan struct{}), prefix: "test "}

	done := make(chan struct{})
	go func() {
		p.handleIncomingConnection(nil, make(chan uint32, 4))
		close(done)
	}()

	client.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleIncomingConnection did not return on a clean client EOF")
	}
}

// TestHandleIncomingConnection_Terminate covers the Terminate-forwards-then-
// returns branch.
func TestHandleIncomingConnection_Terminate(t *testing.T) {
	client, lconn := net.Pipe()
	defer client.Close()
	rconn, rconnPeer := net.Pipe()
	defer rconnPeer.Close()

	p := &Proxy{lconn: lconn, rconn: rconn, errsig: make(chan struct{}), prefix: "test "}

	done := make(chan struct{})
	go func() {
		p.handleIncomingConnection(nil, make(chan uint32, 4))
		close(done)
	}()

	go func() {
		_, _ = client.Write(encodeMsg(&pgproto3.Terminate{}))
	}()

	backend := pgproto3.NewBackend(rconnPeer, rconnPeer)
	msg, err := backend.Receive()
	if err != nil {
		t.Fatalf("failed to read the forwarded message: %v", err)
	}
	if _, ok := msg.(*pgproto3.Terminate); !ok {
		t.Fatalf("got %T, want *pgproto3.Terminate", msg)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleIncomingConnection did not return after forwarding Terminate")
	}
}

// TestHandleResponseConnection_CleanEOF exercises handleResponseConnection
// in isolation with a backend that hangs up with no data in flight (see
// TestHandleIncomingConnection_CleanEOF for why this is ErrUnexpectedEOF,
// not a plain io.EOF, despite the clean close).
func TestHandleResponseConnection_CleanEOF(t *testing.T) {
	lconn, lconnPeer := net.Pipe()
	defer lconnPeer.Close()
	rconn, rconnPeer := net.Pipe()

	p := &Proxy{lconn: lconn, rconn: rconn, errsig: make(chan struct{}), prefix: "test "}

	done := make(chan struct{})
	go func() {
		p.handleResponseConnection(backendTarget{}, make(chan uint32, 4))
		close(done)
	}()

	rconnPeer.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleResponseConnection did not return on a clean backend EOF")
	}
}

// TestHandleResponseConnection_AuthTypeRelay covers every Authentication*
// branch of handleResponseConnection's switch: each message must be
// forwarded to the client unmodified and its AuthType pushed to authTypeCh.
func TestHandleResponseConnection_AuthTypeRelay(t *testing.T) {
	tests := []struct {
		name         string
		msg          pgproto3.BackendMessage
		wantAuthType uint32
	}{
		{"cleartext password", &pgproto3.AuthenticationCleartextPassword{}, pgproto3.AuthTypeCleartextPassword},
		{"md5 password", &pgproto3.AuthenticationMD5Password{Salt: [4]byte{1, 2, 3, 4}}, pgproto3.AuthTypeMD5Password},
		{"sasl", &pgproto3.AuthenticationSASL{AuthMechanisms: []string{"SCRAM-SHA-256"}}, pgproto3.AuthTypeSASL},
		{"sasl continue", &pgproto3.AuthenticationSASLContinue{Data: []byte("x")}, pgproto3.AuthTypeSASLContinue},
		{"sasl final", &pgproto3.AuthenticationSASLFinal{Data: []byte("x")}, pgproto3.AuthTypeSASLFinal},
		{"gss", &pgproto3.AuthenticationGSS{}, pgproto3.AuthTypeGSS},
		{"gss continue", &pgproto3.AuthenticationGSSContinue{Data: []byte("x")}, pgproto3.AuthTypeGSSCont},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lconn, lconnPeer := net.Pipe()
			defer lconn.Close()
			defer lconnPeer.Close()
			rconn, rconnPeer := net.Pipe()
			defer rconn.Close()

			authTypeCh := make(chan uint32, 4)
			p := &Proxy{lconn: lconn, rconn: rconn, errsig: make(chan struct{}), prefix: "test "}

			done := make(chan struct{})
			go func() {
				p.handleResponseConnection(backendTarget{}, authTypeCh)
				close(done)
			}()

			go func() {
				_, _ = rconnPeer.Write(encodeMsg(tt.msg))
			}()

			frontend := pgproto3.NewFrontend(lconnPeer, lconnPeer)
			got, err := frontend.Receive()
			if err != nil {
				t.Fatalf("failed to read the relayed message: %v", err)
			}
			if gotType, wantType := fmt.Sprintf("%T", got), fmt.Sprintf("%T", tt.msg); gotType != wantType {
				t.Errorf("relayed message type = %s, want %s", gotType, wantType)
			}

			select {
			case at := <-authTypeCh:
				if at != tt.wantAuthType {
					t.Errorf("authTypeCh got %d, want %d", at, tt.wantAuthType)
				}
			case <-time.After(time.Second):
				t.Fatal("authTypeCh never received a value")
			}

			rconnPeer.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("handleResponseConnection did not return")
			}
		})
	}
}
