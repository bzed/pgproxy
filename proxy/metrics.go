package proxy

import "sync/atomic"

// Metrics collects counters about proxy activity, giving an operator
// something to monitor without needing a query log (REVIEW.md M8). The
// zero value is ready to use; pass a *Metrics to Start via WithMetrics to
// have it populated as the proxy runs. Safe for concurrent use, including
// reading a Snapshot while sessions are live.
//
// This intentionally exposes raw counters rather than committing to a
// specific export format (Prometheus, StatsD, ...): wrap Snapshot's result
// in whatever format your monitoring stack expects.
type Metrics struct {
	// TotalConnections counts every accepted TCP/unix connection, including
	// ones later rejected by an ACL or the connection cap.
	TotalConnections atomic.Int64
	// ActiveConnections is the current number of sessions that passed
	// admission (ACL, connection cap) and are running - this is also what
	// WithMaxConnections compares against its limit.
	ActiveConnections atomic.Int64
	// RejectedByACL counts connections refused by WithACL.
	RejectedByACL atomic.Int64
	// RejectedByMaxConnections counts connections refused by
	// WithMaxConnections.
	RejectedByMaxConnections atomic.Int64
	// BlockedQueries counts queries a Handler/ContextHandler refused
	// (Handler returned a non-nil error).
	BlockedQueries atomic.Int64
	// BackendConnectErrors counts sessions that failed to reach their
	// configured backend.
	BackendConnectErrors atomic.Int64
}

// MetricsSnapshot is a point-in-time copy of Metrics' counters, safe to
// serialize (e.g. to JSON) or compare, unlike Metrics itself (which holds
// atomics and must not be copied).
type MetricsSnapshot struct {
	TotalConnections         int64
	ActiveConnections        int64
	RejectedByACL            int64
	RejectedByMaxConnections int64
	BlockedQueries           int64
	BackendConnectErrors     int64
}

// The addXxx/loadActiveConnections methods below are nil-safe (a no-op, or
// 0, on a nil *Metrics) so every call site in proxy.go/auth.go can record a
// counter unconditionally: WithMetrics is optional, and a *Proxy built
// directly (as unit tests do, bypassing Start) has a nil metrics field
// rather than Start's internal default.

func (m *Metrics) addTotalConnections(n int64) {
	if m != nil {
		m.TotalConnections.Add(n)
	}
}

func (m *Metrics) addActiveConnections(n int64) {
	if m != nil {
		m.ActiveConnections.Add(n)
	}
}

func (m *Metrics) loadActiveConnections() int64 {
	if m == nil {
		return 0
	}
	return m.ActiveConnections.Load()
}

func (m *Metrics) addRejectedByACL(n int64) {
	if m != nil {
		m.RejectedByACL.Add(n)
	}
}

func (m *Metrics) addRejectedByMaxConnections(n int64) {
	if m != nil {
		m.RejectedByMaxConnections.Add(n)
	}
}

func (m *Metrics) addBlockedQueries(n int64) {
	if m != nil {
		m.BlockedQueries.Add(n)
	}
}

func (m *Metrics) addBackendConnectErrors(n int64) {
	if m != nil {
		m.BackendConnectErrors.Add(n)
	}
}

// Snapshot returns a point-in-time copy of m's counters. Safe to call on a
// nil *Metrics (returns the zero MetricsSnapshot), so callers that only
// sometimes configure WithMetrics don't need to nil-check first.
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	return MetricsSnapshot{
		TotalConnections:         m.TotalConnections.Load(),
		ActiveConnections:        m.ActiveConnections.Load(),
		RejectedByACL:            m.RejectedByACL.Load(),
		RejectedByMaxConnections: m.RejectedByMaxConnections.Load(),
		BlockedQueries:           m.BlockedQueries.Load(),
		BackendConnectErrors:     m.BackendConnectErrors.Load(),
	}
}
