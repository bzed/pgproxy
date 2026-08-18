package cli

import (
	"flag"
	"os"
	"testing"
)

func TestHelp(t *testing.T) {
	// Should just print help to stdout
	help()
}

func TestInfo(t *testing.T) {
	info("localhost:9090")
}

func TestLogDirAndPid(t *testing.T) {
	// Setup log dir
	logDir()

	// Write PID
	saveCurrentPid()

	// Read PID
	pid := getCurrentPid()
	if pid == 0 {
		t.Errorf("getCurrentPid() returned 0")
	}

	// Should not crash when stop is called (will try to kill itself, but since we are running tests, it will just log or exit, actually wait - if it kills itself the test fails!)
	// We'll skip testing stop() directly because it sends SIGKILL to os.Getpid().
}

func TestMainHelp(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"pgproxy", "help"}

	// mock config file creation
	configFile := "test_pgproxy.conf"
	os.WriteFile(configFile, []byte(`
[Server]
ProxyAddr = "localhost:9090"

[DB]
[DB.master]
Addr = "127.0.0.1:5432"
User = "postgres"
Password = "testpass"
DBName = "testdb"
`), 0644)
	defer os.Remove(configFile)

	// Test Main with help
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	Main(configFile, []string{"pgproxy", "help"})

	// Test Main with insufficient args
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	Main(configFile, []string{"pgproxy"})

	// Test stop directly
	os.Remove("./log/pid.log") // Ensure no valid pid
	stop()
}
