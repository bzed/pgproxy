package proxy

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

var (
	testProxyHost     = "127.0.0.1:9090"
	testRemoteHost    = "127.0.0.1:5432"
	testBenchmarkHost = "127.0.0.1:9092"
	testListenerHost  = "127.0.0.1:9091"
)

// dbBackendAvailable does a quick, single-attempt reachability check so
// DB-gated tests can skip immediately in environments without a real
// PostgreSQL, instead of always paying a multi-second fixed sleep first.
func dbBackendAvailable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitForListener polls addr until something is listening or timeout
// elapses, returning whether it came up in time.
func waitForListener(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func Benchmark_Start(b *testing.B) {
	if !dbBackendAvailable(testRemoteHost) {
		b.Skip("PostgreSQL backend not available at", testRemoteHost)
	}

	// Create a simple pass-through handler for benchmarking
	handler := func(query string) ([]byte, error) {
		return []byte(query), nil
	}

	if _, err := Start(testBenchmarkHost, map[string]DBConfig{"testdb": {Addr: testRemoteHost, DBName: "testdb"}}, handler); err != nil {
		b.Fatalf("Failed to start proxy: %v", err)
	}
	if !waitForListener(testBenchmarkHost, 5*time.Second) {
		b.Fatal("proxy did not start listening in time")
	}

	db, err := sqlx.Open("postgres", "host=127.0.0.1 user=postgres password=testpass dbname=testdb port=9092 sslmode=disable")
	if err != nil {
		b.Skip("Database connection failed:", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		b.Skip("Database ping failed:", err)
	}

	db.SetMaxIdleConns(1)
	db.SetMaxOpenConns(100)

	for i := 0; i < b.N; i++ {
		sql := fmt.Sprintf("select id from client where id = %d", i)
		rows, err := db.Query(sql)
		if err != nil {
			b.Error(err)
		}
		if rows != nil {
			rows.Close()
		}
	}
}

func Test_Start(t *testing.T) {
	if !dbBackendAvailable(testRemoteHost) {
		t.Skip("PostgreSQL backend not available at", testRemoteHost)
	}

	// Create a simple pass-through handler
	handler := func(query string) ([]byte, error) {
		return []byte(query), nil
	}

	if _, err := Start(testProxyHost, map[string]DBConfig{"testdb": {Addr: testRemoteHost, DBName: "testdb"}}, handler); err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	if !waitForListener(testProxyHost, 5*time.Second) {
		t.Fatal("proxy did not start listening in time")
	}

	// Try to connect - skip if database not available
	// Use a connection timeout to prevent hanging
	db, err := sqlx.Open("postgres", "host=127.0.0.1 user=postgres password=testpass dbname=testdb port=9090 sslmode=disable connect_timeout=5")
	if err != nil {
		t.Skip("Database connection failed:", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skip("Database ping failed:", err)
	}

	// Set timeouts for database operations
	db.SetMaxIdleConns(1)
	db.SetMaxOpenConns(100)

	// Set a timeout for the query
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, "select 8 as id")
	if err != nil {
		t.Error(err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var n int32
		err = rows.Scan(&n)
		if err != nil {
			t.Error(err)
		} else {
			if n != 8 {
				t.Errorf("result is not match,n=%d but expected 8", n)
			}
		}
	}
}

func Test_getListener(t *testing.T) {
	// Test TCP listener
	l1, err := getListener(testListenerHost)
	if err != nil {
		t.Fatalf("Failed to get TCP listener: %v", err)
	}
	l1.Close()

	// Test Unix listener
	sockPath := "/tmp/test_pgproxy.sock"
	l2, err := getListener(sockPath)
	if err != nil {
		t.Fatalf("Failed to get Unix listener: %v", err)
	}
	defer l2.Close()

	if l2.Addr().Network() != "unix" {
		t.Errorf("Expected unix network, got %s", l2.Addr().Network())
	}
}

func Test_getListener_invalidAddr(t *testing.T) {
	if _, err := getListener("not a valid address"); err == nil {
		t.Error("Expected an error for an invalid listen address, got nil")
	}
}

// Test_Start_InvalidAddress covers Start's own propagation of a
// getListener error (as opposed to getListener's error itself, above).
func Test_Start_InvalidAddress(t *testing.T) {
	if _, err := Start("not a valid address", nil, nil); err == nil {
		t.Error("Expected an error for an invalid listen address, got nil")
	}
}

// Test_Start_GracefulStop confirms that the stop() function returned by
// Start actually closes the listener and lets the accept loop exit (M10):
// stop() calls wg.Wait(), so it hanging means the loop never returned.
func Test_Start_GracefulStop(t *testing.T) {
	stop, err := Start("127.0.0.1:0", map[string]DBConfig{}, nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() did not return - accept loop likely did not exit on listener close")
	}
}

// Test_Start_SystemdNotify covers both outcomes of Start's daemon.SdNotify
// call: a NOTIFY_SOCKET that can't be reached (logged, non-fatal) and a real
// notify socket that actually receives the READY=1 datagram. Systemd
// notification is a Linux-only concept and "unixgram" sockets aren't
// available on Windows, so this is skipped there like the other
// unix-socket tests in this package.
func Test_Start_SystemdNotify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("systemd notification sockets are not supported on Windows")
	}

	t.Run("unreachable NOTIFY_SOCKET does not fail Start", func(t *testing.T) {
		t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.sock"))

		stop, err := Start("127.0.0.1:0", map[string]DBConfig{}, nil)
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		stop()
	})

	t.Run("a real notify socket receives the READY datagram", func(t *testing.T) {
		sockPath := filepath.Join(t.TempDir(), "notify.sock")
		pc, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
		if err != nil {
			t.Fatalf("failed to create notify socket: %v", err)
		}
		defer pc.Close()

		t.Setenv("NOTIFY_SOCKET", sockPath)

		stop, err := Start("127.0.0.1:0", map[string]DBConfig{}, nil)
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer stop()

		buf := make([]byte, 256)
		if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("failed to set read deadline: %v", err)
		}
		n, err := pc.Read(buf)
		if err != nil {
			t.Fatalf("did not receive a systemd notify datagram: %v", err)
		}
		if !strings.Contains(string(buf[:n]), "READY=1") {
			t.Errorf("expected a READY=1 datagram, got %q", buf[:n])
		}
	})
}
