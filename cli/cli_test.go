package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInfo(t *testing.T) {
	info("localhost:9090")
}

// Test_mainBody_version covers Main's -version flag path (REVIEW.md L3: no
// -version flag existed before) via the unit-testable mainBody, since Main
// itself can only be exercised once per test binary (see main_signal_test.go).
func Test_mainBody_version(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	mainBody("", "pgproxy.conf", true, nil)

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("failed to read captured stdout: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != VERSION {
		t.Errorf("mainBody(showVersion=true) printed %q, want %q", got, VERSION)
	}
}

// Test_mainBody_configPathFallback covers mainBody falling back to the
// -config flag's value when the explicit configPath argument is empty.
func Test_mainBody_configPathFallback(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "fallback_test.conf")
	testConfig := `
[ServerConfig]
    ProxyAddr = "127.0.0.1:0"

[DB]
    [DB.master]
        Addr = "127.0.0.1:5432"
        DBName = "testdb"
`
	if err := os.WriteFile(configPath, []byte(testConfig), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	chExit := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mainBody("", configPath, false, chExit)
	}()

	chExit <- os.Interrupt

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mainBody() did not return after a shutdown signal")
	}
}

// Test_run_gracefulShutdown drives run() end-to-end: it starts the proxy on
// an ephemeral port and confirms that a value on chExit makes it return
// (proxy.Start's stop() has been called) instead of blocking forever.
func Test_run_gracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "run_test.conf")
	testConfig := `
[ServerConfig]
    ProxyAddr = "127.0.0.1:0"

[DB]
    [DB.master]
        Addr = "127.0.0.1:5432"
        DBName = "testdb"
`
	if err := os.WriteFile(configPath, []byte(testConfig), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Buffered so the send below never blocks, however far run() has
	// gotten - no sleep-based synchronization needed.
	chExit := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(configPath, chExit)
	}()

	chExit <- os.Interrupt

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after a shutdown signal")
	}
}
