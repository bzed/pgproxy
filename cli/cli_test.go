package cli

import (
	"flag"
	"os"
	"testing"
)


func TestInfo(t *testing.T) {
	info("localhost:9090")
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
}
