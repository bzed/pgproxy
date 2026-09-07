package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgproto3"
)

// DBConfig holds the configuration for a target database.
type DBConfig struct {
	Addr   string
	DBName string

	// TLSRootCert, if set, is a PEM file used to verify the backend's
	// certificate when the backend requests an SSL upgrade (sslmode
	// equivalent to verify-ca/verify-full). ServerName defaults to the
	// host part of Addr but can be overridden with TLSServerName.
	//
	// If TLSRootCert is empty, the link is still encrypted whenever the
	// backend offers SSL, but the certificate is NOT verified (equivalent
	// to libpq's sslmode=require).
	TLSRootCert   string
	TLSServerName string
}

// NewFrontendTLSConfig builds the tls.Config used to terminate TLS on the
// client-facing listener (REVIEW.md H3), for use with WithTLS. certFile/
// keyFile are a PEM certificate and private key. If clientCAFile is
// non-empty, client certificate authentication is required (mutual TLS):
// the client must present a certificate signed by a CA in that file, or the
// handshake fails.
func NewFrontendTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading TLS certificate/key: %w", err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}

	if clientCAFile != "" {
		pem, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("reading TLS client CA %q: %w", clientCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in TLS client CA %q", clientCAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return cfg, nil
}

// backendTarget describes how to reach a specific backend: network ("tcp" or
// "unix") plus the dial address. It is also used to forward CancelRequests
// directly to the backend a session was talking to.
type backendTarget struct {
	network string
	addr    string
}

func targetFor(addr string) backendTarget {
	network := "tcp"
	if strings.HasPrefix(addr, "/") {
		network = "unix"
	} else if strings.HasPrefix(addr, "unix:") {
		network = "unix"
		addr = strings.TrimPrefix(addr, "unix:")
	}
	return backendTarget{network: network, addr: addr}
}

// readStartupMessage reads the initial packet(s) from the client using
// pgproto3's own startup framing (Backend.ReceiveStartupMessage), which
// natively understands StartupMessage, SSLRequest, GSSEncRequest and
// CancelRequest.
//
// If tlsConfig is non-nil, an SSLRequest is accepted ('S') and conn is
// upgraded to TLS (REVIEW.md H3) before the loop continues to read the
// StartupMessage or CancelRequest that follows over the encrypted
// connection; the returned conn is then the TLS-wrapped one, and the caller
// must use it (not the original) for the rest of the session. If tlsConfig
// is nil, SSLRequest is denied ('N') exactly as before. GSSEncRequest is
// always denied - the proxy does not terminate GSS encryption on the client
// side.
//
// The returned message is either *pgproto3.StartupMessage or
// *pgproto3.CancelRequest.
func readStartupMessage(conn net.Conn, tlsConfig *tls.Config) (pgproto3.FrontendMessage, net.Conn, error) {
	backend := pgproto3.NewBackend(conn, conn)
	for {
		msg, err := backend.ReceiveStartupMessage()
		if err != nil {
			return nil, conn, err
		}

		switch msg.(type) {
		case *pgproto3.SSLRequest:
			if tlsConfig == nil {
				if _, err := conn.Write([]byte{'N'}); err != nil {
					return nil, conn, err
				}
				continue
			}
			if _, err := conn.Write([]byte{'S'}); err != nil {
				return nil, conn, err
			}
			tlsConn := tls.Server(conn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return nil, conn, fmt.Errorf("client TLS handshake failed: %w", err)
			}
			conn = tlsConn
			backend = pgproto3.NewBackend(conn, conn)
			continue
		case *pgproto3.GSSEncRequest:
			if _, err := conn.Write([]byte{'N'}); err != nil {
				return nil, conn, err
			}
			continue
		default:
			return msg, conn, nil
		}
	}
}

// rewriteStartupMessage returns a copy of sm with the "database" startup
// parameter rewritten to db.DBName, so the backend sees its own real
// database name rather than the proxy config key the client used to select
// it. All other parameters (including "user") are passed through unchanged:
// pgproxy does not know client passwords and never authenticates on the
// client's behalf, it only routes the connection and lets the client
// authenticate directly against the real backend.
func rewriteStartupMessage(sm *pgproto3.StartupMessage, db DBConfig) *pgproto3.StartupMessage {
	params := make(map[string]string, len(sm.Parameters))
	for k, v := range sm.Parameters {
		params[k] = v
	}
	if db.DBName != "" {
		params["database"] = db.DBName
	}
	return &pgproto3.StartupMessage{
		ProtocolVersion: sm.ProtocolVersion,
		Parameters:      params,
	}
}

// backendTLSConfig builds the tls.Config used to upgrade the connection to a
// backend that answered SSLRequest with 'S'.
func backendTLSConfig(db DBConfig, dialAddr string) (*tls.Config, error) {
	serverName := db.TLSServerName
	if serverName == "" {
		if host, _, err := net.SplitHostPort(dialAddr); err == nil {
			serverName = host
		} else {
			serverName = dialAddr
		}
	}

	conf := &tls.Config{ServerName: serverName}

	if db.TLSRootCert == "" {
		// No CA configured: still encrypt the proxy<->backend link, but we
		// have nothing to verify the certificate against. This matches
		// libpq's sslmode=require. Configure TLSRootCert to get
		// verify-ca/verify-full style verification instead.
		conf.InsecureSkipVerify = true //nolint:gosec // deliberate: no CA configured, see comment above.
		return conf, nil
	}

	pem, err := os.ReadFile(db.TLSRootCert)
	if err != nil {
		return nil, fmt.Errorf("reading TLSRootCert %q: %w", db.TLSRootCert, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in TLSRootCert %q", db.TLSRootCert)
	}
	conf.RootCAs = pool
	return conf, nil
}

// connectBackend connects to the backend database, negotiates SSL if the
// backend requests it, and forwards a (possibly database-rewritten) copy of
// the client's StartupMessage. It leaves the connection ready to be piped:
// the backend's Authentication/ParameterStatus/BackendKeyData/ReadyForQuery
// messages are relayed to the client unmodified by the caller.
func connectBackend(db DBConfig, sm *pgproto3.StartupMessage) (net.Conn, backendTarget, error) {
	target := targetFor(db.Addr)

	conn, err := net.DialTimeout(target.network, target.addr, backendDialTimeout)
	if err != nil {
		return nil, target, err
	}

	// 1. Send SSLRequest
	sslReq := encodeMsg(&pgproto3.SSLRequest{})
	if _, err := conn.Write(sslReq); err != nil {
		conn.Close()
		return nil, target, err
	}
	var sslResp [1]byte
	if _, err := io.ReadFull(conn, sslResp[:]); err != nil {
		conn.Close()
		return nil, target, err
	}
	if sslResp[0] == 'S' {
		tlsConf, err := backendTLSConfig(db, target.addr)
		if err != nil {
			conn.Close()
			return nil, target, err
		}
		conn = tls.Client(conn, tlsConf)
	}

	// 2. Forward the (database-rewritten) StartupMessage.
	backendStartup := rewriteStartupMessage(sm, db)
	if _, err := conn.Write(encodeMsg(backendStartup)); err != nil {
		conn.Close()
		return nil, target, err
	}

	// We are done! Return the connection to the proxy handler so it can
	// seamlessly proxy the Auth requests/responses.
	return conn, target, nil
}
