package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// TestTargetFor covers all three of targetFor's address forms: a plain
// tcp host:port, an absolute unix socket path, and a "unix:"-prefixed path.
func TestTargetFor(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		wantNetwork string
		wantAddr    string
	}{
		{"tcp address", "127.0.0.1:5432", "tcp", "127.0.0.1:5432"},
		{"absolute unix path", "/var/run/postgresql/.s.PGSQL.5432", "unix", "/var/run/postgresql/.s.PGSQL.5432"},
		{"unix: prefix", "unix:/var/run/postgresql/.s.PGSQL.5432", "unix", "/var/run/postgresql/.s.PGSQL.5432"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := targetFor(tt.addr)
			if got.network != tt.wantNetwork || got.addr != tt.wantAddr {
				t.Errorf("targetFor(%q) = %+v, want {%q %q}", tt.addr, got, tt.wantNetwork, tt.wantAddr)
			}
		})
	}
}

// TestReadStartupMessage drives readStartupMessage over a net.Pipe,
// covering StartupMessage, SSLRequest/GSSEncRequest denial-then-continue,
// CancelRequest, and the read-error path.
func TestReadStartupMessage(t *testing.T) {
	t.Run("StartupMessage", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()

		go func() {
			sm := &pgproto3.StartupMessage{
				ProtocolVersion: pgproto3.ProtocolVersionNumber,
				Parameters:      map[string]string{"user": "u", "database": "d"},
			}
			_, _ = client.Write(encodeMsg(sm))
		}()

		msg, err := readStartupMessage(server)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := msg.(*pgproto3.StartupMessage); !ok {
			t.Fatalf("got %T, want *pgproto3.StartupMessage", msg)
		}
	})

	t.Run("SSLRequest is denied then the StartupMessage follows", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()

		go func() {
			_, _ = client.Write(encodeMsg(&pgproto3.SSLRequest{}))
			deny := make([]byte, 1)
			if _, err := io.ReadFull(client, deny); err != nil {
				return
			}
			if deny[0] != 'N' {
				return
			}
			sm := &pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: map[string]string{"user": "u"}}
			_, _ = client.Write(encodeMsg(sm))
		}()

		msg, err := readStartupMessage(server)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := msg.(*pgproto3.StartupMessage); !ok {
			t.Fatalf("got %T, want *pgproto3.StartupMessage", msg)
		}
	})

	t.Run("GSSEncRequest is denied then the StartupMessage follows", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()

		go func() {
			_, _ = client.Write(encodeMsg(&pgproto3.GSSEncRequest{}))
			deny := make([]byte, 1)
			if _, err := io.ReadFull(client, deny); err != nil {
				return
			}
			sm := &pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: map[string]string{"user": "u"}}
			_, _ = client.Write(encodeMsg(sm))
		}()

		msg, err := readStartupMessage(server)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := msg.(*pgproto3.StartupMessage); !ok {
			t.Fatalf("got %T, want *pgproto3.StartupMessage", msg)
		}
	})

	t.Run("CancelRequest", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()

		go func() {
			cr := &pgproto3.CancelRequest{ProcessID: 4242, SecretKey: []byte{1, 2, 3, 4}}
			_, _ = client.Write(encodeMsg(cr))
		}()

		msg, err := readStartupMessage(server)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := msg.(*pgproto3.CancelRequest); !ok {
			t.Fatalf("got %T, want *pgproto3.CancelRequest", msg)
		}
	})

	t.Run("client hangs up before sending anything", func(t *testing.T) {
		client, server := net.Pipe()
		client.Close()

		if _, err := readStartupMessage(server); err == nil {
			t.Error("expected an error when the client closes without sending a startup packet")
		}
	})
}

// generateSelfSignedCert returns a PEM-encoded self-signed certificate/key
// pair valid for commonName, suitable both as a TLS server certificate and
// (since it is self-signed) as its own trusted CA for tests.
func generateSelfSignedCert(t *testing.T, commonName string) (certPEM, keyPEM []byte) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{commonName},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("failed to marshal key: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// TestBackendTLSConfig covers backendTLSConfig's no-CA (encrypted but
// unverified) path, its ServerName derivation, and the TLSRootCert
// (verify-ca) path including its error branches.
func TestBackendTLSConfig(t *testing.T) {
	t.Run("no CA: encrypted but unverified, server name derived from dialAddr", func(t *testing.T) {
		conf, err := backendTLSConfig(DBConfig{}, "db.example.com:5432")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !conf.InsecureSkipVerify {
			t.Error("expected InsecureSkipVerify=true when no CA is configured")
		}
		if conf.ServerName != "db.example.com" {
			t.Errorf("ServerName = %q, want %q", conf.ServerName, "db.example.com")
		}
	})

	t.Run("no CA: explicit TLSServerName overrides the derived host", func(t *testing.T) {
		conf, err := backendTLSConfig(DBConfig{TLSServerName: "override.example.com"}, "db.example.com:5432")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if conf.ServerName != "override.example.com" {
			t.Errorf("ServerName = %q, want %q", conf.ServerName, "override.example.com")
		}
	})

	t.Run("no CA: dialAddr without a port falls back to the address itself", func(t *testing.T) {
		conf, err := backendTLSConfig(DBConfig{}, "/var/run/postgresql/.s.PGSQL.5432")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if conf.ServerName != "/var/run/postgresql/.s.PGSQL.5432" {
			t.Errorf("ServerName = %q, want the raw dialAddr", conf.ServerName)
		}
	})

	t.Run("CA file missing", func(t *testing.T) {
		_, err := backendTLSConfig(DBConfig{TLSRootCert: filepath.Join(t.TempDir(), "nonexistent.pem")}, "host:5432")
		if err == nil {
			t.Error("expected an error for a missing CA file")
		}
	})

	t.Run("CA file has no valid PEM certificates", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad-ca.pem")
		if err := os.WriteFile(bad, []byte("this is not a certificate"), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
		_, err := backendTLSConfig(DBConfig{TLSRootCert: bad}, "host:5432")
		if err == nil {
			t.Error("expected an error for a CA file with no valid certificates")
		}
	})

	t.Run("valid CA file: verification enabled, RootCAs configured", func(t *testing.T) {
		certPEM, _ := generateSelfSignedCert(t, "backend.test")
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, certPEM, 0644); err != nil {
			t.Fatalf("failed to write CA file: %v", err)
		}

		conf, err := backendTLSConfig(DBConfig{TLSRootCert: ca}, "host:5432")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if conf.InsecureSkipVerify {
			t.Error("expected InsecureSkipVerify=false when a CA is configured")
		}
		if conf.RootCAs == nil {
			t.Error("expected RootCAs to be set")
		}
	})
}

// TestConnectBackend covers connectBackend's dial-error and
// read-SSL-response-error branches directly, plus a full successful SSL
// upgrade against a self-signed TLS backend (verify-ca path).
func TestConnectBackend(t *testing.T) {
	sm := &pgproto3.StartupMessage{
		ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters:      map[string]string{"user": "u", "database": "clientkey"},
	}

	t.Run("dial error", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to listen: %v", err)
		}
		addr := ln.Addr().String()
		ln.Close() // nothing listens here anymore

		if _, _, err := connectBackend(DBConfig{Addr: addr}, sm); err == nil {
			t.Error("expected a dial error")
		}
	})

	t.Run("backend closes before answering SSLRequest", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to listen: %v", err)
		}
		defer ln.Close()

		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close() // hang up without responding
		}()

		if _, _, err := connectBackend(DBConfig{Addr: ln.Addr().String()}, sm); err == nil {
			t.Error("expected a read error when the backend hangs up early")
		}
	})

	t.Run("SSL upgrade succeeds and the rewritten StartupMessage is forwarded", func(t *testing.T) {
		certPEM, keyPEM := generateSelfSignedCert(t, "backend.test")
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("failed to load test cert: %v", err)
		}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to listen: %v", err)
		}
		defer ln.Close()

		gotStartup := make(chan *pgproto3.StartupMessage, 1)
		go func() {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			defer raw.Close()

			// The SSLRequest is exactly 8 raw bytes (length + code).
			hdr := make([]byte, 8)
			if _, err := io.ReadFull(raw, hdr); err != nil {
				return
			}
			if _, err := raw.Write([]byte{'S'}); err != nil {
				return
			}

			tlsConn := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}})
			backend := pgproto3.NewBackend(tlsConn, tlsConn)
			msg, err := backend.ReceiveStartupMessage()
			if err != nil {
				return
			}
			if got, ok := msg.(*pgproto3.StartupMessage); ok {
				gotStartup <- got
			}
		}()

		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, certPEM, 0644); err != nil {
			t.Fatalf("failed to write CA file: %v", err)
		}

		db := DBConfig{Addr: ln.Addr().String(), DBName: "realdb", TLSRootCert: ca, TLSServerName: "backend.test"}
		conn, _, err := connectBackend(db, sm)
		if err != nil {
			t.Fatalf("connectBackend failed: %v", err)
		}
		defer conn.Close()

		select {
		case got := <-gotStartup:
			if got.Parameters["database"] != "realdb" {
				t.Errorf("backend saw database=%q, want the rewritten %q", got.Parameters["database"], "realdb")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("backend never received the forwarded StartupMessage")
		}
	})
}
