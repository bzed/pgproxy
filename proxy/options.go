package proxy

import (
	"crypto/tls"
	"time"
)

// ConnInfo carries request-scoped identity for a session to a
// ContextHandler: the proxy-assigned connection id, the "user" and
// "database" parameters the client sent in its StartupMessage (Database is
// the proxy config key the client requested, i.e. dbs's key - not the
// backend's real database name), and the client's remote address.
//
// It is built once, right after the StartupMessage is parsed, and is
// immutable for the life of the session.
type ConnInfo struct {
	ConnID     uint64
	User       string
	Database   string
	RemoteAddr string
}

// ContextHandler is like Handler but additionally receives the session's
// ConnInfo, so a handler can implement per-user/per-database rules, log
// with real identity, or rate-limit - none of which a bare query string can
// express (REVIEW.md M7). Configure one with WithContextHandler; it takes
// priority over the plain Handler passed to Start when both are set.
type ContextHandler func(info ConnInfo, query string) ([]byte, error)

// startConfig collects the optional behavior Option functions configure.
// Its zero value matches Start's behavior before Options existed: no
// frontend TLS, no ACL, no connection cap, no idle timeout.
type startConfig struct {
	tlsConfig      *tls.Config
	acl            *ACL
	contextHandler ContextHandler
	maxConnections int
	idleTimeout    time.Duration
	metrics        *Metrics
}

// Option configures optional Start behavior. See WithTLS, WithACL,
// WithContextHandler, WithMaxConnections, and WithIdleTimeout.
type Option func(*startConfig)

// WithTLS enables frontend TLS: a client's SSLRequest is accepted and the
// connection upgraded using cfg, instead of always being denied (REVIEW.md
// H3). Build cfg with NewFrontendTLSConfig, or construct one directly for
// full control (e.g. a custom GetCertificate callback for SNI).
func WithTLS(cfg *tls.Config) Option {
	return func(c *startConfig) { c.tlsConfig = cfg }
}

// WithACL enables proxy-level access control (REVIEW.md H4): the client's
// source address, StartupMessage "user", and requested database (the dbs
// map key) are each checked against acl before a session is allowed to
// proceed. See ACL's doc comment for the exact matching rules.
func WithACL(acl ACL) Option {
	return func(c *startConfig) { c.acl = &acl }
}

// WithContextHandler sets a ContextHandler, used instead of Start's plain
// Handler argument (REVIEW.md M7). Pass a nil Handler to Start when using
// this exclusively.
func WithContextHandler(h ContextHandler) Option {
	return func(c *startConfig) { c.contextHandler = h }
}

// WithMaxConnections caps the number of concurrent sessions Start will
// service; a connection beyond the cap receives a FATAL ErrorResponse
// (SQLSTATE 53300, matching real PostgreSQL's too_many_connections) and is
// closed (REVIEW.md M4). n <= 0 means unlimited (the default).
func WithMaxConnections(n int) Option {
	return func(c *startConfig) { c.maxConnections = n }
}

// WithIdleTimeout closes a session's client connection if it goes this long
// without sending a complete message (REVIEW.md M4): a client that
// completes startup and then sends nothing would otherwise pin a goroutine
// and a backend connection indefinitely. d <= 0 means no idle timeout (the
// default).
func WithIdleTimeout(d time.Duration) Option {
	return func(c *startConfig) { c.idleTimeout = d }
}

// WithMetrics populates m with counters as the proxy runs - connection
// counts, rejections, blocked queries, backend errors - giving an operator
// something to monitor (REVIEW.md M8). m's zero value is ready to use; read
// it (via m.Snapshot()) at any time, including while Start is still
// running.
func WithMetrics(m *Metrics) Option {
	return func(c *startConfig) { c.metrics = m }
}
