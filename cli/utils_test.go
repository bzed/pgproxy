package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bzed/pgproxy/proxy"
)

// writeSelfSignedCert writes a minimal self-signed cert/key pair to
// certFile/keyFile, for buildStartOptions' TLS test below.
func writeSelfSignedCert(t *testing.T, certFile, keyFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pgproxy.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("failed to marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		t.Fatalf("failed to write cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0644); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}
}

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

func Test_readConfig_malformedTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "bad.conf")
	// Unterminated table header - not valid TOML.
	if err := os.WriteFile(configPath, []byte("[ServerConfig\n"), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	if _, err := readConfig(configPath); err == nil {
		t.Error("Expected an error for malformed TOML, got nil")
	}
}

// Test_readConfig_noMasterRequired covers L2: readConfig must accept a
// config whose only [DB.*] entry is not named "master" - the name "master"
// was never documented and is not special to pgproxy (the client's
// requested database name just needs to match some [DB.*] key).
func Test_readConfig_noMasterRequired(t *testing.T) {
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

	pc, err := readConfig(configPath)
	if err != nil {
		t.Fatalf("readConfig() error = %v, want nil for a config with only DB.reports", err)
	}
	if _, ok := pc.DB["reports"]; !ok {
		t.Error("Expected DB.reports to exist")
	}
}

// Test_readConfig_noDatabasesConfigured covers the actual, documented
// requirement: at least one [DB.*] entry, under any name.
func Test_readConfig_noDatabasesConfigured(t *testing.T) {
	testConfig := `
[ServerConfig]
    ProxyAddr = "127.0.0.1:9090"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.conf")
	if err := os.WriteFile(configPath, []byte(testConfig), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	if _, err := readConfig(configPath); err == nil {
		t.Error("Expected an error when no [DB.*] entries are configured, got nil")
	}
}

// TestBuildStartOptions covers buildStartOptions' mapping from config
// sections to proxy.Option values (REVIEW.md H3/H4/M4): each section
// present contributes exactly one Option, an empty config contributes
// none, and a malformed section is a configuration error rather than a
// silently-ignored one.
func TestBuildStartOptions(t *testing.T) {
	t.Run("empty config produces no options", func(t *testing.T) {
		opts, err := buildStartOptions(ProxyConfig{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 0 {
			t.Errorf("got %d options, want 0", len(opts))
		}
	})

	t.Run("TLSCert/TLSKey produce a TLS option", func(t *testing.T) {
		certFile := filepath.Join(t.TempDir(), "cert.pem")
		keyFile := filepath.Join(t.TempDir(), "key.pem")
		writeSelfSignedCert(t, certFile, keyFile)

		var pc ProxyConfig
		pc.ServerConfig.TLSCert = certFile
		pc.ServerConfig.TLSKey = keyFile

		opts, err := buildStartOptions(pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 1 {
			t.Errorf("got %d options, want 1", len(opts))
		}
	})

	t.Run("an unreadable TLSCert is a configuration error", func(t *testing.T) {
		var pc ProxyConfig
		pc.ServerConfig.TLSCert = filepath.Join(t.TempDir(), "missing.pem")
		pc.ServerConfig.TLSKey = filepath.Join(t.TempDir(), "missing-key.pem")

		if _, err := buildStartOptions(pc); err == nil {
			t.Error("expected an error for a missing TLS cert/key")
		}
	})

	t.Run("a populated ACL produces an ACL option", func(t *testing.T) {
		var pc ProxyConfig
		pc.ACL = proxy.ACL{AllowedUsers: []string{"alice"}}

		opts, err := buildStartOptions(pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 1 {
			t.Errorf("got %d options, want 1", len(opts))
		}
	})

	t.Run("MaxConnections produces a max-connections option", func(t *testing.T) {
		var pc ProxyConfig
		pc.ServerConfig.MaxConnections = 100

		opts, err := buildStartOptions(pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 1 {
			t.Errorf("got %d options, want 1", len(opts))
		}
	})

	t.Run("IdleTimeout produces an idle-timeout option", func(t *testing.T) {
		var pc ProxyConfig
		pc.ServerConfig.IdleTimeout = "5m"

		opts, err := buildStartOptions(pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 1 {
			t.Errorf("got %d options, want 1", len(opts))
		}
	})

	t.Run("an unparseable IdleTimeout is a configuration error", func(t *testing.T) {
		var pc ProxyConfig
		pc.ServerConfig.IdleTimeout = "not a duration"

		if _, err := buildStartOptions(pc); err == nil {
			t.Error("expected an error for an unparseable IdleTimeout")
		}
	})

	t.Run("every section together produces every option", func(t *testing.T) {
		certFile := filepath.Join(t.TempDir(), "cert.pem")
		keyFile := filepath.Join(t.TempDir(), "key.pem")
		writeSelfSignedCert(t, certFile, keyFile)

		var pc ProxyConfig
		pc.ServerConfig.TLSCert = certFile
		pc.ServerConfig.TLSKey = keyFile
		pc.ServerConfig.MaxConnections = 50
		pc.ServerConfig.IdleTimeout = "30s"
		pc.ACL = proxy.ACL{AllowedDatabases: []string{"reports"}}

		opts, err := buildStartOptions(pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 4 {
			t.Errorf("got %d options, want 4", len(opts))
		}
	})
}
