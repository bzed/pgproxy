//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Test_Main_realSignalHandling drives the real Main() entrypoint (flag
// registration/parsing and real OS signal handling), which Test_run_
// gracefulShutdown (cli_test.go) does not exercise since it calls run()
// directly with a synthetic channel. Main can safely be called only once
// per test binary (a second flag.String("config", ...) would panic with
// "flag redefined"), so this is the sole test that invokes it.
//
// It signals its own process with a real SIGINT, which requires syscall.Kill
// and syscall.SIGINT - not portable to Windows, hence the build tag; Main
// keeps its (small) unix-only coverage gap there.
//
// The self-signal is safe here only because of the guard registration
// below: once any channel is registered via signal.Notify for a signal, the
// Go runtime never again falls back to the OS default disposition (process
// termination) for that signal, even during the brief window before Main's
// own signal.Notify call has run. The retry loop then covers that same
// window: it resends SIGINT until Main's handler has registered and the
// shutdown actually completes.
func Test_Main_realSignalHandling(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "main_signal_test.conf")
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

	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	done := make(chan struct{})
	go func() {
		defer close(done)
		Main(configPath)
	}()

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-deadline:
			t.Fatal("Main() did not shut down after SIGINT")
		case <-ticker.C:
			_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
		}
	}
}
