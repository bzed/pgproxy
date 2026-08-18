package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_Main(t *testing.T) {
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

	// Test Main with valid config - should handle "help" command
	// Pass empty args to trigger help
	Main(configPath, []string{"pgproxy"})
}
