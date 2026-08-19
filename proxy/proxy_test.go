package proxy

import (
	"context"
	"fmt"
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

func Benchmark_Start(b *testing.B) {
	// Create a simple pass-through handler for benchmarking
	handler := func(query string) ([]byte, error) {
		return []byte(query), nil
	}

	go Start(testBenchmarkHost, map[string]DBConfig{"testdb": {Addr: testRemoteHost, User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)
	time.Sleep(3 * time.Second)

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
	// Create a simple pass-through handler
	handler := func(query string) ([]byte, error) {
		return []byte(query), nil
	}

	go Start(testProxyHost, map[string]DBConfig{"testdb": {Addr: testRemoteHost, User: "postgres", Password: "testpass", DBName: "testdb"}}, handler)
	// Increase sleep time to ensure proxy is ready
	time.Sleep(5 * time.Second)

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
	l1 := getListener(testListenerHost)
	if l1 == nil {
		t.Fatal("Failed to get TCP listener")
	}
	l1.Close()

	// Test Unix listener
	sockPath := "/tmp/test_pgproxy.sock"
	l2 := getListener(sockPath)
	if l2 == nil {
		t.Fatal("Failed to get Unix listener")
	}
	defer l2.Close()

	if l2.Addr().Network() != "unix" {
		t.Errorf("Expected unix network, got %s", l2.Addr().Network())
	}
}
