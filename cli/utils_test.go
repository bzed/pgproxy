package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_readConfig(t *testing.T) {
	// Create a temporary test config file
	testConfig := `# Test config
[ServerConfig]
    ProxyAddr = "127.0.0.1:9090"

[DB]
    [DB.master]
        Addr = "127.0.0.1:5432"
        User = "postgres"
        Password = "testpass"
        DbName = "testdb"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.conf")
	err := os.WriteFile(configPath, []byte(testConfig), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	pc, connStr := readConfig(configPath)

	if pc.ServerConfig.ProxyAddr != "127.0.0.1:9090" {
		t.Errorf("Expected ProxyAddr 127.0.0.1:9090, got %s", pc.ServerConfig.ProxyAddr)
	}

	if _, ok := pc.DB["master"]; !ok {
		t.Error("Expected DB.master to exist")
	}

	if connStr == "" {
		t.Error("Expected connection string to be non-empty")
	}
}
