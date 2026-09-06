package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInfo(t *testing.T) {
	info("localhost:9090")
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
