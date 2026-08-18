package proxy

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

var (
	testProxyHost  = "127.0.0.1:9090"
	testRemoteHost = "127.0.0.1:5432"
)

func Benchmark_Start(b *testing.B) {
	// Create a simple pass-through handler for benchmarking
	handler := func(query string) ([]byte, error) {
		return []byte(query), nil
	}

	go Start(testProxyHost, testRemoteHost, handler)
	time.Sleep(3 * time.Second)

	db, err := sqlx.Open("postgres", "host=127.0.0.1 user=postgres password=testpass dbname=testdb port=9090 sslmode=disable")
	if err != nil {
		b.Skip("Database connection failed:", err)
	}
	defer db.Close()

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

	go Start(testProxyHost, testRemoteHost, handler)
	time.Sleep(3 * time.Second)

	// Try to connect - skip if database not available
	db, err := sqlx.Open("postgres", "host=127.0.0.1 user=postgres password=testpass dbname=testdb port=9090 sslmode=disable")
	if err != nil {
		t.Skip("Database connection failed:", err)
	}
	defer db.Close()

	db.SetMaxIdleConns(1)
	db.SetMaxOpenConns(100)

	rows, err := db.Query("select 8 as id")
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

func Test_getResolvedAddresses(t *testing.T) {
	getResolvedAddresses("127.0.0.1:9090")
}

func Test_getListener(t *testing.T) {
	paddr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	getListener(paddr)
}
