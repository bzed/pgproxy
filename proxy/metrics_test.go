package proxy

import "testing"

// TestMetrics_Snapshot covers Metrics.Snapshot's nil-receiver safety and
// its normal path.
func TestMetrics_Snapshot(t *testing.T) {
	t.Run("nil *Metrics returns the zero snapshot", func(t *testing.T) {
		var m *Metrics
		if got := (MetricsSnapshot{}); m.Snapshot() != got {
			t.Errorf("nil Metrics.Snapshot() = %+v, want %+v", m.Snapshot(), got)
		}
	})

	t.Run("populated Metrics", func(t *testing.T) {
		var m Metrics
		m.TotalConnections.Store(3)
		m.ActiveConnections.Store(2)
		m.RejectedByACL.Store(1)
		m.RejectedByMaxConnections.Store(4)
		m.BlockedQueries.Store(5)
		m.BackendConnectErrors.Store(6)

		want := MetricsSnapshot{
			TotalConnections:         3,
			ActiveConnections:        2,
			RejectedByACL:            1,
			RejectedByMaxConnections: 4,
			BlockedQueries:           5,
			BackendConnectErrors:     6,
		}
		if got := m.Snapshot(); got != want {
			t.Errorf("Snapshot() = %+v, want %+v", got, want)
		}
	})
}

// TestMetrics_nilSafeHelpers covers the addXxx/loadActiveConnections
// methods' nil-receiver no-op path, which every proxy.go/auth.go call site
// relies on so a *Proxy built without a WithMetrics-configured Start (as
// unit tests routinely do) never panics.
func TestMetrics_nilSafeHelpers(t *testing.T) {
	var m *Metrics
	m.addTotalConnections(1)
	m.addActiveConnections(1)
	m.addRejectedByACL(1)
	m.addRejectedByMaxConnections(1)
	m.addBlockedQueries(1)
	m.addBackendConnectErrors(1)
	if got := m.loadActiveConnections(); got != 0 {
		t.Errorf("loadActiveConnections() on nil = %d, want 0", got)
	}
}
