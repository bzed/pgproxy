package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/jackc/pgproto3/v2"
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
// CancelRequest. SSLRequest and GSSEncRequest are denied ('N') since the
// proxy does not (yet) terminate TLS/GSS on the client side; the loop then
// continues to read the StartupMessage or CancelRequest that follows.
//
// The returned message is either *pgproto3.StartupMessage or
// *pgproto3.CancelRequest.
func readStartupMessage(conn net.Conn) (pgproto3.FrontendMessage, error) {
	backend := pgproto3.NewBackend(pgproto3.NewChunkReader(conn), conn)
	for {
		msg, err := backend.ReceiveStartupMessage()
		if err != nil {
			return nil, err
		}

		switch msg.(type) {
		case *pgproto3.SSLRequest, *pgproto3.GSSEncRequest:
			if _, err := conn.Write([]byte{'N'}); err != nil {
				return nil, err
			}
			continue
		default:
			return msg, nil
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

	conn, err := net.Dial(target.network, target.addr)
	if err != nil {
		return nil, target, err
	}

	// 1. Send SSLRequest
	sslReq := (&pgproto3.SSLRequest{}).Encode(nil)
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
	if _, err := conn.Write(backendStartup.Encode(nil)); err != nil {
		conn.Close()
		return nil, target, err
	}

	// We are done! Return the connection to the proxy handler so it can
	// seamlessly proxy the Auth requests/responses.
	return conn, target, nil
}
