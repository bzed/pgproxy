package parser

import (
	"strings"
	"testing"
)

// mustFilter is a helper: filter.Filter(query) must equal want, else the
// test fails with the query included in the message.
func mustFilter(t *testing.T, filter *QueryFilter, query string, want bool) {
	t.Helper()
	if got := filter.Filter([]byte(query)); got != want {
		t.Errorf("Filter(%q) = %v, want %v", query, got, want)
	}
}

func TestQueryFilter(t *testing.T) {
	config := DefaultFilterConfig()
	filter := NewQueryFilter(config)

	if !filter.Filter([]byte("select a from b")) {
		t.Errorf("Filter valid select failed")
	}

	if !filter.Filter([]byte("select * from b")) {
		t.Errorf("Filter select * should be allowed now by default")
	}

	if !filter.Filter([]byte("delete from a where id = 1")) {
		t.Errorf("Filter delete with where failed")
	}

	if filter.Filter([]byte("delete from a")) {
		t.Errorf("Filter unbounded delete should return false")
	}

	if filter.Filter([]byte("truncate table a")) {
		t.Errorf("Filter truncate should return false")
	}

	if !filter.Filter([]byte("insert into a(id) values(1)")) {
		t.Errorf("Filter insert failed")
	}

	if !filter.Filter([]byte("update a set b=1 where id = 1")) {
		t.Errorf("Filter bounded update failed")
	}

	if filter.Filter([]byte("update a set b=1")) {
		t.Errorf("Filter unbounded update should return false")
	}

	if filter.Filter([]byte("ALTER USER bob WITH PASSWORD 'newpass'")) {
		t.Errorf("Filter alter role should return false by default")
	}

	if filter.Filter([]byte("select * from")) {
		t.Errorf("Filter invalid syntax should fail")
	}
}

func TestQueryFilterConfig(t *testing.T) {
	config := DefaultFilterConfig()
	config.AllowSelect = false
	config.RequireWhereForDelete = false
	config.AllowAlterRole = true

	filter := NewQueryFilter(config)

	if filter.Filter([]byte("select * from a")) {
		t.Errorf("Filter should block select when AllowSelect is false")
	}

	if !filter.Filter([]byte("delete from a")) {
		t.Errorf("Filter should allow unbounded delete when RequireWhereForDelete is false")
	}

	if !filter.Filter([]byte("ALTER ROLE bob WITH PASSWORD 'newpass'")) {
		t.Errorf("Filter should allow alter role when AllowAlterRole is true")
	}
}

func TestQueryFilterSignatures(t *testing.T) {
	config := DefaultFilterConfig()
	config.SignatureFilterEnabled = true
	config.SignatureAllowByDefault = true
	config.BlockSignatures = []string{
		"SELECT * FROM users WHERE (id = _) AND (name = _)",
	}

	filter := NewQueryFilter(config)

	// Blocked by signature
	if filter.Filter([]byte("SELECT * FROM users WHERE id = 123 AND name = 'alice'")) {
		t.Errorf("Filter should block query matching BlockSignatures")
	}
	if filter.Filter([]byte("SELECT * FROM users WHERE id = 456 AND name = 'bob'")) {
		t.Errorf("Filter should block query matching BlockSignatures regardless of literals")
	}

	// Allowed because signature differs
	if !filter.Filter([]byte("SELECT * FROM users WHERE id = 123")) {
		t.Errorf("Filter should allow queries with different signatures")
	}

	// Test AllowSignatures strictly
	config2 := DefaultFilterConfig()
	config2.SignatureFilterEnabled = true
	config2.AllowSignatures = []string{
		"SELECT id FROM allowed_table",
	}
	filter2 := NewQueryFilter(config2)

	if !filter2.Filter([]byte("SELECT id FROM allowed_table")) {
		t.Errorf("Filter should allow query in AllowSignatures")
	}
	if filter2.Filter([]byte("SELECT id, name FROM allowed_table")) {
		t.Errorf("Filter should block query NOT in AllowSignatures when AllowSignatures is populated")
	}
}

// TestFilter_OnParseErrorPolicy covers the three OnParseError policies:
// "" (default, treated as "block"), "allow", and "audit".
func TestFilter_OnParseErrorPolicy(t *testing.T) {
	const garbage = "select * from" // unparseable

	tests := []struct {
		name         string
		onParseError string
		want         bool
	}{
		{"default (empty) blocks", "", false},
		{"explicit block", "block", false},
		{"allow lets it through", "allow", true},
		{"audit lets it through and logs", "audit", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultFilterConfig()
			config.OnParseError = tt.onParseError
			filter := NewQueryFilter(config)
			mustFilter(t, filter, garbage, tt.want)
		})
	}
}

// TestFilter_SignatureAuditMode checks that a signature match in audit mode
// logs instead of blocking, and the statement then falls through to the
// normal per-statement-type rules.
func TestFilter_SignatureAuditMode(t *testing.T) {
	config := DefaultFilterConfig()
	config.SignatureFilterEnabled = true
	config.SignatureAuditMode = true
	config.BlockSignatures = []string{"SELECT * FROM users WHERE (id = _)"}

	filter := NewQueryFilter(config)

	// Matches a "blocked" signature, but audit mode does not enforce it -
	// the query is still allowed by the (default) per-statement-type rules.
	mustFilter(t, filter, "SELECT * FROM users WHERE id = 123", true)
}

// TestFilter_BypassPrevention exercises extractStatements: mutations hidden
// inside EXPLAIN, PREPARE, or a CTE must still be evaluated against the
// per-statement-type rules, not silently passed through.
func TestFilter_BypassPrevention(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		config func() FilterConfig
		want   bool
	}{
		{
			name:  "EXPLAIN hides an unbounded delete",
			query: "EXPLAIN DELETE FROM a",
			config: func() FilterConfig {
				return DefaultFilterConfig() // RequireWhereForDelete defaults true
			},
			want: false,
		},
		{
			name:  "EXPLAIN of a properly bounded delete is allowed",
			query: "EXPLAIN DELETE FROM a WHERE id = 1",
			config: func() FilterConfig {
				return DefaultFilterConfig()
			},
			want: true,
		},
		{
			name:  "PREPARE hides a disallowed truncate",
			query: "PREPARE stmt1 AS TRUNCATE TABLE a",
			config: func() FilterConfig {
				return DefaultFilterConfig() // AllowTruncate defaults false
			},
			want: false,
		},
		{
			name:  "PREPARE of an allowed statement passes through",
			query: "PREPARE stmt1 AS DELETE FROM a WHERE id = 1",
			config: func() FilterConfig {
				return DefaultFilterConfig()
			},
			want: true,
		},
		{
			name:  "CTE hides an unbounded delete inside a SELECT",
			query: "WITH cte AS (DELETE FROM a) SELECT * FROM cte",
			config: func() FilterConfig {
				return DefaultFilterConfig()
			},
			want: false,
		},
		{
			name:  "extractStatements recurses into an INSERT's CTE",
			query: "WITH cte AS (SELECT 1) INSERT INTO a SELECT * FROM cte",
			config: func() FilterConfig {
				c := DefaultFilterConfig()
				c.AllowInsert = false
				return c
			},
			want: false, // the top-level Insert itself is disallowed
		},
		{
			name:  "extractStatements recurses into an UPDATE's CTE without breaking a legitimate query",
			query: "WITH cte AS (SELECT 1) UPDATE a SET b = 1 WHERE id IN (SELECT * FROM cte)",
			config: func() FilterConfig {
				return DefaultFilterConfig() // has its own WHERE, should be allowed
			},
			want: true,
		},
		{
			name:  "CTE hides an unbounded delete inside a DELETE",
			query: "WITH cte AS (DELETE FROM a) DELETE FROM b WHERE id IN (SELECT * FROM cte)",
			config: func() FilterConfig {
				return DefaultFilterConfig() // outer delete has WHERE, inner cte delete does not
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewQueryFilter(tt.config())
			mustFilter(t, filter, tt.query, tt.want)
		})
	}
}

// TestFilter_BypassPrevention_NestedSubqueries covers REVIEW.md C1: a
// data-modifying CTE hidden inside a subquery (FROM, an INSERT source, a
// scalar SET expression, or a CREATE TABLE AS source), not just at the
// statement's own top level. All four must be blocked when AllowDelete is
// false, exactly like a bare top-level DELETE would be.
func TestFilter_BypassPrevention_NestedSubqueries(t *testing.T) {
	config := DefaultFilterConfig()
	config.AllowDelete = false

	filter := NewQueryFilter(config)

	queries := []string{
		"SELECT * FROM (WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d) sub",
		"INSERT INTO t SELECT * FROM (WITH d AS (DELETE FROM s RETURNING *) SELECT * FROM d) sub",
		"UPDATE t SET x = (WITH d AS (DELETE FROM s RETURNING *) SELECT * FROM d)",
		"CREATE TABLE c AS WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			mustFilter(t, filter, q, false)
		})
	}
}

// TestFilter_ExecuteAndSetVar covers the AllowExecute/AllowSetVar switches.
func TestFilter_ExecuteAndSetVar(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		allow   bool
		enabled func(c *FilterConfig, v bool)
		want    bool
	}{
		{"execute blocked by default", "EXECUTE stmt1", false, func(c *FilterConfig, v bool) { c.AllowExecute = v }, false},
		{"execute allowed when enabled", "EXECUTE stmt1", true, func(c *FilterConfig, v bool) { c.AllowExecute = v }, true},
		{"set blocked by default", "SET search_path = public", false, func(c *FilterConfig, v bool) { c.AllowSetVar = v }, false},
		{"set allowed when enabled", "SET search_path = public", true, func(c *FilterConfig, v bool) { c.AllowSetVar = v }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultFilterConfig()
			tt.enabled(&config, tt.allow)
			filter := NewQueryFilter(config)
			mustFilter(t, filter, tt.query, tt.want)
		})
	}
}

// TestRedactSecrets covers REVIEW.md M9: PASSWORD/IDENTIFIED BY literals
// must not appear verbatim in redacted output.
func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			"ALTER ROLE PASSWORD",
			"ALTER ROLE bob WITH PASSWORD 'hunter2'",
			"ALTER ROLE bob WITH PASSWORD '***REDACTED***'",
		},
		{
			"CREATE USER IDENTIFIED BY",
			"CREATE USER bob IDENTIFIED BY 'hunter2'",
			"CREATE USER bob IDENTIFIED BY '***REDACTED***'",
		},
		{
			"no secret clause is untouched",
			"SELECT * FROM users WHERE name = 'bob'",
			"SELECT * FROM users WHERE name = 'bob'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactSecrets(tt.query); got != tt.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", tt.query, got, tt.want)
			}
			if strings.Contains(redactSecrets(tt.query), "hunter2") {
				t.Errorf("redactSecrets(%q) leaked the plaintext secret: %q", tt.query, redactSecrets(tt.query))
			}
		})
	}
}

// TestFilter_DefaultAllowsCommonSetVar covers REVIEW.md M1: the default
// config must allow the SET statements common drivers/ORMs send at connect
// time (pgjdbc's extra_float_digits, ActiveRecord's client_min_messages,
// SET TIME ZONE, ...), while still blocking the privilege-relevant GUCs via
// BlockSetVars.
func TestFilter_DefaultAllowsCommonSetVar(t *testing.T) {
	filter := NewQueryFilter(DefaultFilterConfig())

	allowed := []string{
		"SET extra_float_digits = 3",
		"SET client_min_messages = warning",
		"SET TIME ZONE 'UTC'",
		"SET application_name = 'myapp'",
		"RESET ALL",
	}
	for _, q := range allowed {
		t.Run(q, func(t *testing.T) { mustFilter(t, filter, q, true) })
	}

	blocked := []string{
		"SET ROLE TO admin",
		"SET role = 'admin'",
		"SET session_authorization = 'admin'",
		"SET session_authorization TO 'admin'",
	}
	for _, q := range blocked {
		t.Run(q, func(t *testing.T) { mustFilter(t, filter, q, false) })
	}
}

// TestFilter_UnknownStatementTypeAllowedByDefault checks that a statement
// type the switch doesn't explicitly handle (e.g. CREATE TABLE) is allowed
// by default, per the documented "unmatched statements pass through" scope.
func TestFilter_UnknownStatementTypeAllowedByDefault(t *testing.T) {
	filter := NewQueryFilter(DefaultFilterConfig())
	mustFilter(t, filter, "CREATE TABLE foo (id int)", true)
}

// TestQueryFilter_Handler covers QueryFilter.Handler's pass-through and
// blocked paths.
func TestQueryFilter_Handler(t *testing.T) {
	filter := NewQueryFilter(DefaultFilterConfig())

	out, err := filter.Handler("select a from b")
	if err != nil {
		t.Fatalf("Handler() unexpected error: %v", err)
	}
	if string(out) != "select a from b" {
		t.Errorf("Handler() = %q, want unchanged query", out)
	}

	if _, err := filter.Handler("delete from a"); err == nil {
		t.Error("Handler() expected an error for a blocked query, got nil")
	}
}

// TestWarnIfFilterConfigIsUnsafe covers both the "blocks everything" warning
// case and every safe configuration that must not warn.
func TestWarnIfFilterConfigIsUnsafe(t *testing.T) {
	tests := []struct {
		name     string
		config   FilterConfig
		wantWarn bool
	}{
		{"signature filter disabled", FilterConfig{SignatureFilterEnabled: false}, false},
		{
			"allow by default",
			FilterConfig{SignatureFilterEnabled: true, SignatureAllowByDefault: true},
			false,
		},
		{
			"has an allow list",
			FilterConfig{SignatureFilterEnabled: true, SignatureAllowByDefault: false, AllowSignatures: []string{"x"}},
			false,
		},
		{
			"blocks everything: enabled, deny by default, empty allow list",
			FilterConfig{SignatureFilterEnabled: true, SignatureAllowByDefault: false},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warned := false
			WarnIfFilterConfigIsUnsafe(tt.config, func(format string, args ...interface{}) { warned = true })
			if warned != tt.wantWarn {
				t.Errorf("WarnIfFilterConfigIsUnsafe warned = %v, want %v", warned, tt.wantWarn)
			}
		})
	}
}
