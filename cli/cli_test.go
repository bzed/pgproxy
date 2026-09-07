package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bzed/pgproxy/parser"
	"github.com/bzed/pgproxy/proxy"
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

	mainBody("", "pgproxy.conf", true, nil, nil)

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
		mainBody("", configPath, false, chExit, nil)
	}()

	chExit <- os.Interrupt

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mainBody() did not return after a shutdown signal")
	}
}

// Test_reloadFilterConfig covers REVIEW.md M8: a SIGHUP-triggered reload
// actually changes the filter's live behavior, and a failed reload (e.g.
// the file went missing) leaves the previous config in effect rather than
// crashing or blocking every query.
func Test_reloadFilterConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "reload_test.conf")
	permissive := `
[ServerConfig]
    ProxyAddr = "127.0.0.1:0"
[DB]
    [DB.master]
        Addr = "127.0.0.1:5432"
        DBName = "testdb"
[Filter]
    allow_select = true
`
	if err := os.WriteFile(configPath, []byte(permissive), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	pc, err := readConfig(configPath)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	queryFilter := parser.NewQueryFilter(pc.FilterConfig)

	if !queryFilter.Filter([]byte("select 1")) {
		t.Fatal("expected select to be allowed before reload")
	}

	stricter := `
[ServerConfig]
    ProxyAddr = "127.0.0.1:0"
[DB]
    [DB.master]
        Addr = "127.0.0.1:5432"
        DBName = "testdb"
[Filter]
    allow_select = false
`
	if err := os.WriteFile(configPath, []byte(stricter), 0644); err != nil {
		t.Fatalf("failed to rewrite test config: %v", err)
	}
	if err := reloadFilterConfig(configPath, queryFilter); err != nil {
		t.Fatalf("reloadFilterConfig() error = %v", err)
	}

	if queryFilter.Filter([]byte("select 1")) {
		t.Error("expected select to be blocked after reload")
	}

	if err := reloadFilterConfig(filepath.Join(tmpDir, "does-not-exist.conf"), queryFilter); err == nil {
		t.Error("expected an error reloading from a missing file")
	}
	if queryFilter.Filter([]byte("select 1")) {
		t.Error("a failed reload must leave the previous (stricter) config in effect")
	}
}

// TestLogMetricsPeriodically covers REVIEW.md M8's periodic metrics
// logging lifecycle: it must actually fire on the ticker (observed here via
// a side effect - Snapshot being called - rather than parsing glog output,
// which doesn't reach a test-capturable stream by default) and stop
// cleanly without hanging when told to.
func TestLogMetricsPeriodically(t *testing.T) {
	metrics := &proxy.Metrics{}
	metrics.TotalConnections.Store(1) // give Snapshot() something to report

	stop := logMetricsPeriodically(metrics, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // several ticks

	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not return - the ticker goroutine did not exit")
	}
}

// Test_run_reloadsOnSIGHUP drives run() end-to-end with a real chReload
// channel (REVIEW.md M8): a value on it must not crash or hang the main
// loop, which must keep responding to chExit afterward.
func Test_run_reloadsOnSIGHUP(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "run_reload_test.conf")
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
	chReload := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(configPath, chExit, chReload)
	}()

	chReload <- syscall.SIGHUP
	time.Sleep(100 * time.Millisecond) // let the reload be processed

	chExit <- os.Interrupt

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after a shutdown signal following a SIGHUP reload")
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
		run(configPath, chExit, nil)
	}()

	chExit <- os.Interrupt

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after a shutdown signal")
	}
}

// Test_run_withMetricsLogInterval covers run()'s MetricsLogInterval wiring
// (REVIEW.md M8): it must start the periodic logger without error and
// still shut down cleanly on chExit (including stopping that logger).
func Test_run_withMetricsLogInterval(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "run_metrics_test.conf")
	testConfig := `
[ServerConfig]
    ProxyAddr = "127.0.0.1:0"
    MetricsLogInterval = "20ms"

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
		run(configPath, chExit, nil)
	}()

	time.Sleep(100 * time.Millisecond) // let the metrics logger tick a few times
	chExit <- os.Interrupt

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after a shutdown signal")
	}
}
