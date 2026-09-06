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
        DBName = "testdb"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.conf")
	err := os.WriteFile(configPath, []byte(testConfig), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	pc, err := readConfig(configPath)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}

	if pc.ServerConfig.ProxyAddr != "127.0.0.1:9090" {
		t.Errorf("Expected ProxyAddr 127.0.0.1:9090, got %s", pc.ServerConfig.ProxyAddr)
	}

	master, ok := pc.DB["master"]
	if !ok {
		t.Fatal("Expected DB.master to exist")
	}
	if master.DBName != "testdb" {
		t.Errorf("Expected DB.master.DBName testdb, got %s", master.DBName)
	}
}

func Test_readConfig_missingFile(t *testing.T) {
	if _, err := readConfig(filepath.Join(t.TempDir(), "does-not-exist.conf")); err == nil {
		t.Error("Expected an error for a missing config file, got nil")
	}
}

func Test_readConfig_missingMaster(t *testing.T) {
	testConfig := `
[ServerConfig]
    ProxyAddr = "127.0.0.1:9090"
[DB]
    [DB.reports]
        Addr = "127.0.0.1:5432"
        DBName = "testdb"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.conf")
	if err := os.WriteFile(configPath, []byte(testConfig), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	if _, err := readConfig(configPath); err == nil {
		t.Error("Expected an error when DB.master is missing, got nil")
	}
}
