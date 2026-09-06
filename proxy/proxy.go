// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package proxy provides proxy service and redirects requests
// form proxy.Addr to remote.Addr.
package proxy

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/golang/glog"
	"github.com/jackc/pgx/v5/pgproto3"
)

var (
	connid uint64 // Self-increasing ConnectID, accessed only via atomic.
)

// Handler function from proxy to postgresql for rewrite
// request or sql. Receives the query string and returns modified bytes.
type Handler func(query string) ([]byte, error)

// backendSession records how to reach the backend a live connection is
// talking to, keyed by the BackendKeyData (PID/secret) the backend assigned
// it. It lets a later CancelRequest, which arrives on a brand new
// connection, be forwarded to the right backend.
type backendSession struct {
	target    backendTarget
	secretKey [4]byte
}

type cancelKey struct {
	pid    uint32
	secret [4]byte
}

var cancelRegistry sync.Map // map[cancelKey]backendSession

// Start proxy server needed receive proxyHost, and database configs.
// It binds the listener synchronously and returns immediately; connections
// are accepted in a background goroutine. The returned stop function closes
// the listener and stops accepting new connections, for graceful shutdown.
func Start(proxyHost string, dbs map[string]DBConfig, handler Handler) (stop func(), err error) {
	glog.Infof("Proxying from %v with %d configured databases\n", proxyHost, len(dbs))

	listener, err := getListener(proxyHost)
	if err != nil {
		return nil, err
	}

	// Notify systemd that the service is ready
	if ok, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		glog.Errorf("Failed to notify systemd: %v", err)
	} else if ok {
		glog.Infof("Systemd notified successfully")
	}

	stopping := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-stopping:
					// Expected: listener was closed for shutdown.
					return
				default:
					glog.Errorf("Failed to accept connection '%s'\n", err)
					continue
				}
			}
			id := atomic.AddUint64(&connid, 1)

			p := &Proxy{
				lconn:  conn,
				errsig: make(chan struct{}),
				prefix: fmt.Sprintf("Connection #%03d ", id),
				connID: id,
			}
			go p.service(dbs, handler)
		}
	}()

	stop = func() {
		close(stopping)
		listener.Close()
		wg.Wait()
	}
	return stop, nil
}

// Listener of a net.Addr.
func getListener(host string) (net.Listener, error) {
	// First, check for systemd socket activation
	listeners, err := activation.Listeners()
	if err == nil && len(listeners) > 0 {
		if len(listeners) > 1 {
			glog.Warningf("Systemd passed %d activated sockets, using only the first; ProxyAddr %q is ignored",
				len(listeners), host)
		} else {
			glog.Infof("Using systemd socket activation; ProxyAddr %q is ignored", host)
		}
		return listeners[0], nil
	}

	var listener net.Listener
	if strings.HasPrefix(host, "/") || strings.HasPrefix(host, "unix:") {
		host = strings.TrimPrefix(host, "unix:")
		listener, err = net.Listen("unix", host)
	} else {
		listener, err = net.Listen("tcp", host)
	}
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", host, err)
	}
	return listener, nil
}

// Proxy - Manages a Proxy connection, piping data between proxy and remote.
type Proxy struct {
	lconn, rconn net.Conn
	lconnMutex   sync.Mutex
	errOnce      sync.Once
	errsig       chan struct{}
	prefix       string
	connID       uint64
}

// New - Create a new Proxy instance. Takes over local connection passed in,
// and closes it when finished.
func New(conn net.Conn, connid uint64) *Proxy {
	return &Proxy{
		lconn:  conn,
		errsig: make(chan struct{}),
		prefix: fmt.Sprintf("Connection #%03d ", connid),
		connID: connid,
	}
}

// err records the first error for this connection and unblocks service().
// It is safe to call concurrently and safe to call more than once: only the
// first call logs and signals, later calls are no-ops (previously this used
// a plain bool guard plus an unbuffered channel send, which both raced
// across the two pipe goroutines and deadlocked forever whenever both
// directions failed at once, since only one of them could ever receive on
// errsig).
func (p *Proxy) err(msg string, err error) {
	p.errOnce.Do(func() {
		if err != nil && err != io.EOF {
			glog.Errorf("%s%s: %v", p.prefix, msg, err)
		} else if err == nil {
			glog.Errorf("%s%s", p.prefix, msg)
		}
		close(p.errsig)
	})
}

// writeError sends a pgproto3 ErrorResponse to the client. Best-effort: the
// connection may already be broken, in which case the write error is
// ignored since the caller is about to tear the session down anyway.
func (p *Proxy) writeError(severity, message string, code string) {
	p.lconnMutex.Lock()
	defer p.lconnMutex.Unlock()
	_, _ = p.lconn.Write(buildErrorResponse(severity, message, code))
}

func buildErrorResponse(severity, message string, code string) []byte {
	if code == "" {
		code = "XX000"
	}
	errResp := &pgproto3.ErrorResponse{
		Severity: severity,
		Message:  message,
		Code:     code,
	}
	return encodeMsg(errResp)
}

// Proxy.service open connection to remote and service proxying data.
func (p *Proxy) service(dbs map[string]DBConfig, handler Handler) {
	defer p.lconn.Close()

	msg, err := readStartupMessage(p.lconn)
	if err != nil {
		p.err("Failed to read startup message", err)
		return
	}

	switch sm := msg.(type) {
	case *pgproto3.CancelRequest:
		p.forwardCancelRequest(sm)
	case *pgproto3.StartupMessage:
		p.serviceStartup(sm, dbs, handler)
	default:
		p.err("Unsupported startup message", fmt.Errorf("%T", msg))
	}
}

// forwardCancelRequest handles a CancelRequest, which PostgreSQL clients
// send on a brand new connection (never on the connection running the
// query). It looks up which backend the (PID, secret) pair belongs to -
// recorded from that connection's BackendKeyData - and forwards the raw
// CancelRequest to that same backend, exactly as a direct client would.
func (p *Proxy) forwardCancelRequest(cr *pgproto3.CancelRequest) {
	v, ok := cancelRegistry.Load(cancelKey{pid: cr.ProcessID, secret: *(*[4]byte)(cr.SecretKey)})
	if !ok {
		glog.Warningf("%sCancelRequest for unknown backend PID %d", p.prefix, cr.ProcessID)
		return
	}
	sess := v.(backendSession)
	if sess.secretKey != *(*[4]byte)(cr.SecretKey) {
		glog.Warningf("%sCancelRequest secret mismatch for backend PID %d", p.prefix, cr.ProcessID)
		return
	}

	conn, err := net.Dial(sess.target.network, sess.target.addr)
	if err != nil {
		glog.Errorf("%sCancelRequest: failed to reach backend: %v", p.prefix, err)
		return
	}
	defer conn.Close()
	if _, err := conn.Write(encodeMsg(cr)); err != nil {
		glog.Errorf("%sCancelRequest: failed to send: %v", p.prefix, err)
	}
}

func (p *Proxy) serviceStartup(sm *pgproto3.StartupMessage, dbs map[string]DBConfig, handler Handler) {
	dbName := sm.Parameters["database"]
	dbConf, ok := dbs[dbName]
	if !ok {
		p.writeError("FATAL", "database not found in proxy config: "+dbName, "3D000")
		p.err("Database not configured: "+dbName, nil)
		return
	}

	rconn, target, err := connectBackend(dbConf, sm)
	if err != nil {
		p.writeError("FATAL", "backend connection failed: "+err.Error(), "08006")
		p.err("Remote connection failed", err)
		return
	}
	p.rconn = rconn
	defer p.rconn.Close()

	// authTypeCh carries the authentication type the backend just told the
	// client about (Authentication{Cleartext,MD5,SASL...}) from the
	// response-relaying goroutine to the request-relaying goroutine, which
	// needs it to correctly disambiguate the client's next 'p' message
	// (PasswordMessage vs SASL{Initial,}Response). Channel send/receive
	// give the required happens-before edge; a shared field guarded only by
	// protocol ordering would be a data race.
	authTypeCh := make(chan uint32, 4)

	var pidRegistered atomic.Uint32 // 0 = not yet, else the registered PID+1

	go p.handleIncomingConnection(handler, authTypeCh)
	go p.handleResponseConnection(target, authTypeCh, &pidRegistered)

	// wait for close...
	<-p.errsig

	if pid := pidRegistered.Load(); pid != 0 {
		cancelRegistry.Delete(pid - 1)
	}
}

// handleIncomingConnection relays client -> backend messages, decoding just
// enough (via pgproto3.Backend) to apply handler to the SQL text of Query
// and Parse messages. All other message types (Bind included) are decoded
// and losslessly re-encoded unmodified, so binary parameters, OIDs and
// portal/statement names are never mangled.
func (p *Proxy) handleIncomingConnection(handler Handler, authTypeCh <-chan uint32) {
	backend := pgproto3.NewBackend(p.lconn, p.lconn)

	for {
		// Drain any authentication-type updates the response side learned
		// about, so Backend.Receive can correctly decode the client's next
		// 'p' message.
		for drained := false; !drained; {
			select {
			case at := <-authTypeCh:
				_ = backend.SetAuthType(at)
			default:
				drained = true
			}
		}

		msg, err := backend.Receive()
		if err != nil {
			if err == io.EOF {
				p.err("Client closed connection", err)
			} else {
				p.err("Read from client failed", err)
			}
			return
		}

		out, herr := p.applyFrontendHandler(msg, handler)
		if herr != nil {
			p.writeError("ERROR", herr.Error(), "42501")
			// Keep session alive for blocked queries (H1)
			if _, ok := msg.(*pgproto3.Query); ok {
				p.lconnMutex.Lock()
				_, _ = p.lconn.Write(encodeMsg(&pgproto3.ReadyForQuery{TxStatus: 'I'}))
				p.lconnMutex.Unlock()
			}
			continue
		}

		if _, err := p.rconn.Write(out); err != nil {
			p.err("Write to backend failed", err)
			return
		}

		if _, ok := msg.(*pgproto3.Terminate); ok {
			return
		}
	}
}

// applyFrontendHandler runs handler over the SQL text of Query and Parse
// messages and re-encodes the (possibly rewritten) message. Every other
// message type is passed through as decoded/re-encoded verbatim.
func (p *Proxy) applyFrontendHandler(msg pgproto3.FrontendMessage, handler Handler) ([]byte, error) {
	switch m := msg.(type) {
	case *pgproto3.Query:
		text, err := runHandler(handler, m.String)
		if err != nil {
			return nil, err
		}
		return encodeMsg(&pgproto3.Query{String: text}), nil
	case *pgproto3.Parse:
		text, err := runHandler(handler, m.Query)
		if err != nil {
			return nil, err
		}
		out := &pgproto3.Parse{Name: m.Name, Query: text, ParameterOIDs: m.ParameterOIDs}
		return encodeMsg(out), nil
	default:
		return encodeMsg(msg), nil
	}
}

// runHandler calls handler with query, tolerating a nil handler (pure
// passthrough) and a handler that signals "no change" via a nil result.
func runHandler(handler Handler, query string) (string, error) {
	if handler == nil {
		return query, nil
	}
	out, err := handler(query)
	if err != nil {
		return "", fmt.Errorf("handler error: %w", err)
	}
	if out == nil {
		return query, nil
	}
	return string(out), nil
}

// handleResponseConnection relays backend -> client messages. It decodes
// each message (via pgproto3.Frontend) just enough to observe
// Authentication requests (to unblock the request side's decoding of the
// client's reply) and BackendKeyData (to support CancelRequest), then
// forwards every message re-encoded verbatim.
func (p *Proxy) handleResponseConnection(target backendTarget, authTypeCh chan<- uint32, pidRegistered *atomic.Uint32) {
	frontend := pgproto3.NewFrontend(p.rconn, p.rconn)

	for {
		msg, err := frontend.Receive()
		if err != nil {
			if err == io.EOF {
				p.err("Backend closed connection", err)
			} else {
				p.err("Read from backend failed", err)
			}
			return
		}

		switch m := msg.(type) {
		case *pgproto3.AuthenticationCleartextPassword:
			authTypeCh <- pgproto3.AuthTypeCleartextPassword
		case *pgproto3.AuthenticationMD5Password:
			authTypeCh <- pgproto3.AuthTypeMD5Password
		case *pgproto3.AuthenticationSASL:
			authTypeCh <- pgproto3.AuthTypeSASL
		case *pgproto3.AuthenticationSASLContinue:
			authTypeCh <- pgproto3.AuthTypeSASLContinue
		case *pgproto3.AuthenticationSASLFinal:
			authTypeCh <- pgproto3.AuthTypeSASLFinal
		case *pgproto3.AuthenticationGSS:
			authTypeCh <- pgproto3.AuthTypeGSS
		case *pgproto3.AuthenticationGSSContinue:
			authTypeCh <- pgproto3.AuthTypeGSSCont
		case *pgproto3.BackendKeyData:
			cancelRegistry.Store(
				cancelKey{pid: m.ProcessID, secret: *(*[4]byte)(m.SecretKey)},
				backendSession{target: target, secretKey: *(*[4]byte)(m.SecretKey)},
			)
			pidRegistered.Store(m.ProcessID + 1)
		}

		p.lconnMutex.Lock()
		_, err = p.lconn.Write(encodeMsg(msg))
		p.lconnMutex.Unlock()
		if err != nil {
			p.err("Write to client failed", err)
			return
		}
	}
}

func encodeMsg(msg pgproto3.Message) []byte {
	b, _ := msg.Encode(nil)
	return b
}
