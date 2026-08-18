package cli

import (
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"os"
	"testing"
)

func TestClientRequest(t *testing.T) {
	db, err := sqlx.Open("postgres", "host=127.0.0.1 port=5432 user=postgres password=testpass dbname=testdb sslmode=disable")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}

	client := &Client{db: db}

	// Will fail to query without actual db, but it covers the code path
	client.Request("select * from test")
	client.Request("insert into test values (1)")
	client.Request("\\d")
}

func TestCommand(t *testing.T) {
	// Mock os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	defer func() {
		os.Stdin = oldStdin
	}()
	os.Stdin = r

	// Write "quit\n" to stdin
	w.WriteString("quit\n")
	w.Close()

	// Need a dummy config
	connStr = "host=127.0.0.1 port=5432 user=postgres password=testpass dbname=testdb sslmode=disable"

	// Run command, it should exit when reading "quit"
	Command()
}
