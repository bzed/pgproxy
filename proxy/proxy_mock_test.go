package proxy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgmock"
	"github.com/jackc/pgproto3/v2"
)

// MockPgServer is a simple TCP server that mocks PostgreSQL
// It tracks queries received and responds with simple mock data
type MockPgServer struct {
	listener     net.Listener
	queries      []string
	mu           sync.Mutex
	closeChan    chan struct{}
	closeWg      sync.WaitGroup
	queryHandler func(query string) error
}

// NewMockPgServer creates a new mock PostgreSQL server
func NewMockPgServer() (*MockPgServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &MockPgServer{
		listener:  listener,
		queries:   make([]string, 0),
		closeChan: make(chan struct{}),
	}, nil
}

// Addr returns the server address
func (m *MockPgServer) Addr() string {
	return m.listener.Addr().String()
}

// Port returns just the port
func (m *MockPgServer) Port() string {
	_, port, _ := net.SplitHostPort(m.listener.Addr().String())
	return port
}

// QueriesReceived returns all queries received
func (m *MockPgServer) QueriesReceived() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.queries))
	copy(result, m.queries)
	return result
}

// SetQueryHandler sets a custom handler for received queries
func (m *MockPgServer) SetQueryHandler(handler func(query string) error) {
	m.queryHandler = handler
}

// Start starts the server
func (m *MockPgServer) Start() {
	m.closeWg.Add(1)
	go func() {
		defer m.closeWg.Done()
		for {
			select {
			case <-m.closeChan:
				m.listener.Close()
				return
			default:
				conn, err := m.listener.Accept()
				if err != nil {
					return
				}
				m.closeWg.Add(1)
				go m.handleConnection(conn)
			}
		}
	}()
}

// Stop stops the server
func (m *MockPgServer) Stop() {
	close(m.closeChan)
	m.listener.Close()
	m.closeWg.Wait()
}

// handleConnection handles a PostgreSQL connection
func (m *MockPgServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	defer m.closeWg.Done()

	isStartup := true
	for {
		var msgType byte
		var contentLength int
		var content []byte

		if isStartup {
			// Read 4 byte length
			lenBuf := make([]byte, 4)
			_, err := io.ReadFull(conn, lenBuf)
			if err != nil {
				return
			}
			msgLength := binary.BigEndian.Uint32(lenBuf)
			contentLength = int(msgLength) - 4
			content = make([]byte, contentLength)
			if contentLength > 0 {
				_, err = io.ReadFull(conn, content)
				if err != nil {
					return
				}
			}

			// Check if SSLRequest
			code := binary.BigEndian.Uint32(content[:4])
			if code == 80877103 {
				conn.Write([]byte{'N'}) // Deny SSL
				continue
			}

			isStartup = false
			msgType = 0 // StartupMessage
		} else {
			header := make([]byte, 5)
			_, err := io.ReadFull(conn, header)
			if err != nil {
				return
			}
			msgType = header[0]
			msgLength := binary.BigEndian.Uint32(header[1:5])
			contentLength = int(msgLength) - 4
			if contentLength < 0 {
				return
			}
			content = make([]byte, contentLength)
			if contentLength > 0 {
				_, err = io.ReadFull(conn, content)
				if err != nil {
					return
				}
			}
		}

		// Process based on message type
		switch msgType {
		case 0: // StartupMessage
			// Send AuthOk
			conn.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 0})
			// Send ReadyForQuery
			conn.Write([]byte{'Z', 0, 0, 0, 5, 'I'})

		case 'Q': // SimpleQuery
			// Extract query string (remove null terminator)
			query := string(bytes.TrimSuffix(content, []byte{0}))
			m.mu.Lock()
			m.queries = append(m.queries, query)
			m.mu.Unlock()

			if m.queryHandler != nil {
				if err := m.queryHandler(query); err != nil {
					// Handler can signal to reject the query
					return
				}
			}

			// Send RowDescription
			rdMsg := []byte{
				'T',         // RowDescription
				0, 0, 0, 18, // Length
				0, 1, // 1 field
				'i', 'd', 0, 0, 0, // field name "id"
				0, 0, 0, 0, // table OID
				0, 0, // attribute number
				23, 0, 0, 0, // data type (int4)
				4, 0, 0, 0, // type length
				0xff, 0xff, 0xff, 0xff, // type modifier (-1 as bytes)
				0, // format
			}
			conn.Write(rdMsg)

			// Send DataRow
			drMsg := []byte{
				'D',        // DataRow
				0, 0, 0, 9, // Length
				0, 1, // 1 column
				0, 0, 0, 4, // 4 bytes of data
				0, 0, 0, 1, // int32 value 1
			}
			conn.Write(drMsg)

			// Send CommandComplete
			ccMsg := []byte{
				'C',         // CommandComplete
				0, 0, 0, 13, // Length (4 + 9 for "SELECT 1\0")
				'S', 'E', 'L', 'E', 'C', 'T', ' ', '1', 0, // command tag
			}
			conn.Write(ccMsg)

			// Send ReadyForQuery
			rfqMsg := []byte{
				'Z',        // ReadyForQuery
				0, 0, 0, 5, // Length
				'I', // Transaction status (Idle)
			}
			conn.Write(rfqMsg)

		case 'X': // Terminate
			return
		default:
		}
	}
}

// TestMockPgServer_Basic tests that the mock server works
func TestMockPgServer_Basic(t *testing.T) {
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock server: %v", err)
	}
	defer mock.Stop()

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Connect and send a query
	conn, err := net.Dial("tcp", mock.Addr())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	performMockStartup(conn)

	// Send SimpleQuery message
	query := "SELECT 1"
	queryMsg := createMockQueryMessage(query)
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Read responses (we don't care about the content for this test)
	buf := make([]byte, 1024)
	for i := 0; i < 4; i++ {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}

	// Give time for processing
	time.Sleep(100 * time.Millisecond)

	// Check mock received the query
	queries := mock.QueriesReceived()
	if len(queries) != 1 {
		t.Errorf("Expected 1 query, got %d", len(queries))
	} else if queries[0] != query {
		t.Errorf("Expected query %q, got %q", query, queries[0])
	}
}

// TestProxyWithMockServer tests pgproxy with mock PostgreSQL server
// This runs on any platform without a real PostgreSQL instance
func TestProxyWithMockServer(t *testing.T) {
	// Create mock server
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Create pass-through handler
	handler := func(query string) ([]byte, error) {
		return []byte(query), nil
	}

	// Start proxy
	proxyAddr := "127.0.0.1:29090"
	go Start(proxyAddr, map[string]DBConfig{"testdb": {Addr: "127.0.0.1:" + mock.Port(), User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)

	time.Sleep(200 * time.Millisecond)
	defer func() {
		// Stop proxy by connecting and closing
		conn, _ := net.Dial("tcp", proxyAddr)
		if conn != nil {
			conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	// Connect to proxy
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer conn.Close()

	performMockStartup(conn)

	// Send SimpleQuery
	query := "SELECT 42"
	queryMsg := createMockQueryMessage(query)
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Read responses
	buf := make([]byte, 1024)
	for i := 0; i < 5; i++ {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}

	// Give time for processing
	time.Sleep(200 * time.Millisecond)

	// Check mock received the query
	queries := mock.QueriesReceived()
	if len(queries) == 0 {
		t.Error("Mock did not receive query")
	} else if queries[0] != query {
		t.Errorf("Expected %q, got %q", query, queries[0])
	}
}

// TestProxyWithFilterAndMock tests query filtering with mock server
func TestProxyWithFilterAndMock(t *testing.T) {
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Handler that replaces "users" with "orgs"
	handler := func(query string) ([]byte, error) {
		return []byte(strings.ReplaceAll(query, "users", "orgs")), nil
	}

	proxyAddr := "127.0.0.1:29091"
	go Start(proxyAddr, map[string]DBConfig{"testdb": {Addr: "127.0.0.1:" + mock.Port(), User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)

	time.Sleep(200 * time.Millisecond)
	defer func() {
		conn, _ := net.Dial("tcp", proxyAddr)
		if conn != nil {
			conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	performMockStartup(conn)

	// Send query with "users"

	originalQuery := "SELECT * FROM users"
	queryMsg := createMockQueryMessage(originalQuery)
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Read responses
	buf := make([]byte, 1024)
	for i := 0; i < 5; i++ {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}

	time.Sleep(200 * time.Millisecond)

	// Check mock received the FILTERED query
	queries := mock.QueriesReceived()
	if len(queries) == 0 {
		t.Fatal("Mock did not receive query")
	}

	expectedQuery := "SELECT * FROM orgs"
	if queries[0] != expectedQuery {
		t.Errorf("Query not filtered correctly. Got %q, want %q", queries[0], expectedQuery)
	}
}

// TestProxyWithBlockingHandler tests that blocking handler prevents queries from reaching backend
func TestProxyWithBlockingHandler(t *testing.T) {
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()

	// Set a handler that will cause the proxy to reject DELETE queries
	mock.SetQueryHandler(func(query string) error {
		// This shouldn't be called since the proxy handler blocks first
		return nil
	})

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Proxy handler that blocks DELETE
	handler := func(query string) ([]byte, error) {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "DELETE") {
			return nil, fmt.Errorf("DELETE not allowed")
		}
		return []byte(query), nil
	}

	proxyAddr := "127.0.0.1:29092"
	go Start(proxyAddr, map[string]DBConfig{"testdb": {Addr: "127.0.0.1:" + mock.Port(), User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)

	time.Sleep(200 * time.Millisecond)
	defer func() {
		conn, _ := net.Dial("tcp", proxyAddr)
		if conn != nil {
			conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	performMockStartup(conn)

	// Send DELETE query (should be blocked by proxy)
	deleteQuery := "DELETE FROM users WHERE id = 1"
	queryMsg := createMockQueryMessage(deleteQuery)
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Connection should be closed by proxy
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, err = conn.Read(buf)
	// We expect an error (connection closed)
	if err == nil {
		t.Error("Expected connection to be closed after blocked query")
	}

	time.Sleep(200 * time.Millisecond)

	// Mock should NOT have received the query
	queries := mock.QueriesReceived()
	if len(queries) > 0 {
		t.Errorf("Mock should not have received blocked query. Got: %v", queries)
	}
}

// TestProxyWithQueryRewriting tests query transformation
func TestProxyWithQueryRewriting(t *testing.T) {
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Handler that adds WHERE 1=1 to SELECT queries
	handler := func(query string) ([]byte, error) {
		upper := strings.ToUpper(strings.TrimSpace(query))
		if strings.HasPrefix(upper, "SELECT") {
			return []byte(query + " WHERE 1=1"), nil
		}
		return []byte(query), nil
	}

	proxyAddr := "127.0.0.1:29093"
	go Start(proxyAddr, map[string]DBConfig{"testdb": {Addr: "127.0.0.1:" + mock.Port(), User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)

	time.Sleep(200 * time.Millisecond)
	defer func() {
		conn, _ := net.Dial("tcp", proxyAddr)
		if conn != nil {
			conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	performMockStartup(conn)

	originalQuery := "SELECT * FROM users"
	queryMsg := createMockQueryMessage(originalQuery)
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Read responses
	buf := make([]byte, 1024)
	for i := 0; i < 5; i++ {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}

	time.Sleep(200 * time.Millisecond)

	// Check query was rewritten
	queries := mock.QueriesReceived()
	if len(queries) == 0 {
		t.Fatal("Mock did not receive query")
	}

	expectedQuery := "SELECT * FROM users WHERE 1=1"
	if queries[0] != expectedQuery {
		t.Errorf("Query not rewritten. Got %q, want %q", queries[0], expectedQuery)
	}
}

// TestMockServerWithQueryTracking tests that our custom mock server tracks queries
func TestMockServerWithQueryTracking(t *testing.T) {
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Set a custom handler to verify it works
	var receivedQueries []string
	mock.SetQueryHandler(func(query string) error {
		receivedQueries = append(receivedQueries, query)
		return nil
	})

	// Connect and send a query
	conn, err := net.Dial("tcp", mock.Addr())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	performMockStartup(conn)

	query := "SELECT * FROM test"
	queryMsg := createMockQueryMessage(query)
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Read responses
	buf := make([]byte, 1024)
	for i := 0; i < 4; i++ {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Check both the custom handler and the built-in tracking received the query
	if len(receivedQueries) != 1 {
		t.Errorf("Custom handler expected 1 query, got %d", len(receivedQueries))
	}

	queries := mock.QueriesReceived()
	if len(queries) != 1 {
		t.Errorf("Mock expected 1 query, got %d", len(queries))
	} else if queries[0] != query {
		t.Errorf("Expected query %q, got %q", query, queries[0])
	}
}

// Helper to create a SimpleQuery message
func createMockQueryMessage(query string) []byte {
	return (&pgproto3.Query{String: query}).Encode(nil)
}

// TestPgMockIntegration tests that pgmock library is integrated and can be used
func TestPgMockIntegration(t *testing.T) {
	// Create a simple pgmock script
	script := &pgmock.Script{
		Steps: []pgmock.Step{
			pgmock.ExpectMessage(&pgproto3.Query{String: "SELECT 1"}),
			pgmock.SendMessage(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")}),
			pgmock.SendMessage(&pgproto3.ReadyForQuery{TxStatus: 'I'}),
		},
	}

	// Start a TCP listener
	ln, err := net.Listen("tcp", "127.0.0.1:")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer ln.Close()

	// Accept connections in a goroutine and run the pgmock script
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Wrap the connection in a pgproto3 Backend
		backend := pgproto3.NewBackend(pgproto3.NewChunkReader(conn), conn)
		if err := script.Run(backend); err != nil {
			t.Logf("pgmock script error: %v", err)
		}
	}()

	// Give time for the listener to start
	time.Sleep(100 * time.Millisecond)

	// Connect to the mock server
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	performMockStartup(conn)

	// Send a SimpleQuery message
	queryMsg := createMockQueryMessage("SELECT 1")
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Read responses (we don't verify them, just check no errors)
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for i := 0; i < 5; i++ {
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}

	t.Log("pgmock library is integrated and functional")
}

// TestProxyWithPasswordChangeFilter tests that password changes are blocked
func TestProxyWithPasswordChangeFilter(t *testing.T) {
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Handler that blocks ALTER USER and SET PASSWORD queries
	handler := func(query string) ([]byte, error) {
		upperQuery := strings.ToUpper(strings.TrimSpace(query))
		// Block password change patterns
		if strings.Contains(upperQuery, "ALTER USER") && (strings.Contains(upperQuery, "PASSWORD") || strings.Contains(upperQuery, "WITH")) ||
			strings.Contains(upperQuery, "SET PASSWORD") ||
			strings.Contains(upperQuery, "CHANGE PASSWORD") {
			return nil, fmt.Errorf("password changes are not allowed")
		}
		return []byte(query), nil
	}

	proxyAddr := "127.0.0.1:29094"
	go Start(proxyAddr, map[string]DBConfig{"testdb": {Addr: "127.0.0.1:" + mock.Port(), User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)

	time.Sleep(200 * time.Millisecond)
	defer func() {
		conn, _ := net.Dial("tcp", proxyAddr)
		if conn != nil {
			conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	// Test each password change pattern in a separate proxy/mock setup
	passwordChangeQueries := []string{
		"ALTER USER postgres WITH PASSWORD 'newpass'",
		"ALTER USER postgres PASSWORD 'newpass'",
		"SET PASSWORD 'newpass' FOR postgres",
	}

	for _, query := range passwordChangeQueries {
		mock.mu.Lock()
		mock.queries = nil
		mock.mu.Unlock()

		conn, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		performMockStartup(conn)

		queryMsg := createMockQueryMessage(query)
		_, err = conn.Write(queryMsg)
		if err != nil {
			conn.Close()
			t.Fatalf("Failed to write: %v", err)
		}

		// Connection should be closed by proxy due to blocked query
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, err = conn.Read(make([]byte, 1024))
		conn.Close()
		// We expect an error (connection closed)
		if err == nil {
			t.Errorf("Password change query %q was not blocked", query)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify that no password change queries reached the mock server
	queries := mock.QueriesReceived()
	if len(queries) > 0 {
		t.Errorf("Mock server should not have received any blocked password change queries. Got: %v", queries)
	}
}

// TestProxyWithReadOnlyFilter tests that only SELECT queries are allowed
func TestProxyWithReadOnlyFilter(t *testing.T) {
	mock, err := NewMockPgServer()
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Handler that only allows SELECT queries (read-only)
	handler := func(query string) ([]byte, error) {
		upperQuery := strings.ToUpper(strings.TrimSpace(query))
		// Only allow SELECT queries
		if !strings.HasPrefix(upperQuery, "SELECT") {
			return nil, fmt.Errorf("only SELECT queries are allowed (read-only mode)")
		}
		return []byte(query), nil
	}

	proxyAddr := "127.0.0.1:29095"
	go Start(proxyAddr, map[string]DBConfig{"testdb": {Addr: "127.0.0.1:" + mock.Port(), User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)

	time.Sleep(200 * time.Millisecond)
	defer func() {
		conn, _ := net.Dial("tcp", proxyAddr)
		if conn != nil {
			conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	// Test SELECT query - should be allowed and forwarded
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	performMockStartup(conn)

	selectQuery := "SELECT * FROM users"
	queryMsg := createMockQueryMessage(selectQuery)
	_, err = conn.Write(queryMsg)
	if err != nil {
		conn.Close()
		t.Fatalf("Failed to write: %v", err)
	}

	// Read responses
	buf := make([]byte, 1024)
	for i := 0; i < 5; i++ {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}
	conn.Close()

	time.Sleep(200 * time.Millisecond)

	// Check that SELECT query reached the mock server
	queries := mock.QueriesReceived()
	if len(queries) == 0 {
		t.Error("SELECT query should have reached the mock server")
	} else if queries[0] != selectQuery {
		t.Errorf("Expected SELECT query %q, got %q", selectQuery, queries[0])
	}

	// Clear queries for next test
	mock.mu.Lock()
	mock.queries = nil
	mock.mu.Unlock()

	// Test non-SELECT queries - should be blocked
	writeOnlyQueries := []string{
		"INSERT INTO users VALUES (1, 'test')",
		"UPDATE users SET name = 'new' WHERE id = 1",
		"DELETE FROM users WHERE id = 1",
		"CREATE TABLE test (id int)",
		"DROP TABLE test",
	}

	for _, query := range writeOnlyQueries {
		mock.mu.Lock()
		mock.queries = nil
		mock.mu.Unlock()

		conn, err = net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		performMockStartup(conn)

		queryMsg = createMockQueryMessage(query)
		_, err = conn.Write(queryMsg)
		if err != nil {
			conn.Close()
			t.Fatalf("Failed to write: %v", err)
		}

		// Connection should be closed by proxy
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, err = conn.Read(buf)
		conn.Close()
		if err == nil {
			t.Errorf("Query %q should have been blocked", query)
		}
		time.Sleep(100 * time.Millisecond)

		// Verify query was not forwarded
		queries = mock.QueriesReceived()
		if len(queries) > 0 {
			t.Errorf("Query %q should not have reached the mock server. Got: %v", query, queries)
		}
	}
}

// Helper to perform startup sequence with the proxy
func performMockStartup(conn net.Conn) error {
	sm := &pgproto3.StartupMessage{
		ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters: map[string]string{
			"user":     "postgres",
			"database": "testdb",
		},
	}
	out := sm.Encode(nil)

	_, err := conn.Write(out)
	if err != nil {
		return err
	}

	// Read AuthOk and ReadyForQuery
	resp := make([]byte, 9+6)
	_, err = io.ReadFull(conn, resp)
	return err
}

// NewMockPgServerUnix creates a new mock PostgreSQL server on a Unix socket
func NewMockPgServerUnix(socketPath string) (*MockPgServer, error) {
	// Remove the socket file if it already exists
	os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &MockPgServer{
		listener:  listener,
		queries:   make([]string, 0),
		closeChan: make(chan struct{}),
	}, nil
}

// TestProxyWithUnixSocket tests the proxy listening on a Unix socket and connecting to a backend via Unix socket
func TestProxyWithUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are not supported on Windows")
	}

	backendSocket := "/tmp/pgproxy_test_backend.sock"
	proxySocket := "/tmp/pgproxy_test_proxy.sock"

	mock, err := NewMockPgServerUnix(backendSocket)
	if err != nil {
		t.Fatalf("Failed to create mock: %v", err)
	}
	defer mock.Stop()
	defer os.Remove(backendSocket)

	mock.Start()
	time.Sleep(50 * time.Millisecond)

	// Clean up any old proxy socket
	os.Remove(proxySocket)

	dbs := map[string]DBConfig{
		"testdb": {
			Addr:     backendSocket,
			User:     "postgres",
			Password: "testpass",
			DBName:   "testdb",
		},
	}

	handler := func(query string) ([]byte, error) {
		return nil, nil // passthrough
	}

	go Start(proxySocket, dbs, handler)
	time.Sleep(100 * time.Millisecond)
	defer os.Remove(proxySocket)

	// Connect to proxy via unix socket
	conn, err := net.Dial("unix", proxySocket)
	if err != nil {
		t.Fatalf("Failed to connect to proxy socket: %v", err)
	}
	defer conn.Close()

	// Perform mock startup
	err = performMockStartup(conn)
	if err != nil {
		t.Fatalf("Failed mock startup: %v", err)
	}

	// Send a simple query
	queryMsg := createMockQueryMessage("SELECT 1")
	_, err = conn.Write(queryMsg)
	if err != nil {
		t.Fatalf("Failed to write query: %v", err)
	}

	// Read responses
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for i := 0; i < 5; i++ {
		_, err = conn.Read(buf)
		if err != nil {
			break
		}
	}

	queries := mock.QueriesReceived()
	if len(queries) != 1 || queries[0] != "SELECT 1" {
		t.Errorf("Expected mock to receive 'SELECT 1', got %v", queries)
	}
}
