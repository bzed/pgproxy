![pgproxy](./pgproxy.png)

# pgproxy
[![Build Status](https://github.com/bzed/pgproxy/actions/workflows/go-test.yml/badge.svg?branch=master)](https://github.com/bzed/pgproxy/actions/workflows/go-test.yml)
[![codecov](https://codecov.io/gh/bzed/pgproxy/branch/master/graph/badge.svg)](https://codecov.io/gh/bzed/pgproxy)
[![GoDoc](https://pkg.go.dev/badge/github.com/bzed/pgproxy.svg)](https://pkg.go.dev/github.com/bzed/pgproxy)
[![License](https://img.shields.io/badge/LICENSE-Apache2.0-ff69b4.svg)](http://www.apache.org/licenses/LICENSE-2.0.html)

pgproxy is a PostgreSQL proxy server that uses pipe redirect connections to filter requested SQL statements. In the future it will support multi-database backup, distributed database adaptation, and other features beyond SQL analysis.

## Features

* Proxy database connections
* Configurable SQL Firewall (block un-safe mutations and destructive queries)
* Signature-based query filtering (pgBouncer style fingerprinting)
* Real PostgreSQL parsing engine (`libpg_query` via `pg_query_go`, not a reimplementation)

## Installation

```
$ go get -u github.com/bzed/pgproxy
```

## Using

### As a separate application

```
$ pgproxy -config /etc/pgproxy/pgproxy.conf
```

pgproxy runs in the foreground and shuts down gracefully on SIGINT/SIGTERM
(it also notifies systemd of readiness and of shutdown when run under
`Type=notify`, see `systemd/pgproxy.service` / `debian/pgproxy.service`).
There is no separate `start`/`stop`/`cli` subcommand.

### Using Configuration

pgproxy is configured using a TOML file (default: `pgproxy.conf`).

pgproxy never learns client passwords: it authenticates connections by
routing the client's `StartupMessage` (with the `database` parameter
rewritten to the backend's real database name) to the target backend and
then relaying the authentication handshake unmodified, so the client
authenticates directly against the backend's own credentials. Because of
this, `[DB.*]` entries only need `Addr` and `DBName` - there is no `User`/
`Password` to configure here.

```toml
[ServerConfig]
    # The address and port pgproxy listens on for incoming connections
    # You can also listen on a unix socket: ProxyAddr = "/tmp/.s.PGSQL.5432"
    ProxyAddr = "127.0.0.1:9090"

    # Terminate TLS on ProxyAddr (see "Frontend TLS" below).
    # TLSCert = "/etc/pgproxy/server.crt"
    # TLSKey = "/etc/pgproxy/server.key"
    # TLSClientCA = "/etc/pgproxy/client-ca.crt"

    # Cap concurrent sessions and/or close idle ones (see "Connection
    # limits" below).
    # MaxConnections = 100
    # IdleTimeout = "5m"

    # Log a metrics snapshot periodically (see "Metrics" below).
    # MetricsLogInterval = "1m"

[ACL]
    # Restrict which clients may connect at all (see "Access control" below).
    # allowed_cidrs = ["10.0.0.0/8"]
    # allowed_users = ["app_user"]
    # allowed_databases = ["master", "reports"]

[DB]
    [DB.master]
        Addr = "127.0.0.1:5432"
        DBName = "testdb"

    [DB.reports]
        Addr = "10.0.0.5:5432"
        DBName = "reportsdb"
        # Verify the backend's certificate against a CA instead of just
        # encrypting the link (see "Backend TLS" below).
        TLSRootCert = "/etc/pgproxy/reports-ca.pem"
        # TLSServerName = "reports.internal"

    [DB.local_socket]
        Addr = "/var/run/postgresql/.s.PGSQL.5432" # Connect to backend via Unix Socket
        DBName = "postgres"

[Filter]
    allow_select = true
    allow_insert = true
    allow_update = true
    allow_delete = true

    allow_truncate = false
    allow_alter_role = false

    # See "SET/RESET" and "EXECUTE" below.
    allow_set_var = true
    block_set_vars = ["session_authorization", "role"]
    allow_execute = false

    require_where_for_update = true
    require_where_for_delete = true

    # See "Statements the parser can't handle" below.
    on_parse_error = "block"

    # block_signatures = ["SELECT * FROM users WHERE id = $1 AND name = $2"]
    # allow_signatures = ["SELECT id FROM allowed_table"]
```

#### Backend TLS

If a backend answers with SSL support, pgproxy always encrypts the
proxy<->backend link. By default it does not verify the backend's
certificate (equivalent to libpq's `sslmode=require`), because most
deployments point at a backend on trusted infrastructure without a CA-issued
cert. Set `TLSRootCert` (a PEM file) on a `[DB.*]` entry to verify the
backend's certificate against that CA instead (equivalent to
`verify-ca`/`verify-full`); `TLSServerName` overrides the hostname checked
against the certificate when it differs from `Addr`.

#### Frontend TLS

By default a client's SSLRequest is denied and the client<->proxy link is
plaintext. Set `TLSCert`/`TLSKey` (a PEM certificate and private key) on
`[ServerConfig]` to terminate TLS on the listener instead: pgproxy then
accepts SSLRequest and upgrades the connection before continuing the
session. Set `TLSClientCA` (a PEM CA bundle) as well to additionally require
mutual TLS - the client must present a certificate signed by that CA, or the
handshake fails.

Programmatically (see "Be called as a package" below), build the config
with `proxy.NewFrontendTLSConfig` and pass it via `proxy.WithTLS`.

#### Access control

The `[ACL]` section restricts which clients may open a session at all,
independent of the SQL filter: `allowed_cidrs` checks the client's source
address (at accept time, before anything else), `allowed_users` checks the
StartupMessage `user` parameter, and `allowed_databases` checks the
requested database (a `[DB.*]` key). Each is an allowlist - a session must
match every *non-empty* one; an absent or empty list imposes no restriction
for that dimension, and an empty `[ACL]` section (or omitting it) allows
every client, same as before this feature existed. A rejected session gets
a FATAL `ErrorResponse` (SQLSTATE `28000` for a source-address rejection,
`42501` for a user/database one) and is closed.

Programmatically, pass a `proxy.ACL` via `proxy.WithACL`.

#### Connection limits

`MaxConnections` (default: unlimited) caps how many sessions may run
concurrently; a connection past the cap gets a FATAL `ErrorResponse`
(SQLSTATE `53300`, matching real PostgreSQL's `too_many_connections`) and is
closed rather than serviced. `IdleTimeout` (default: none) closes a session
that goes that long without sending a complete message - useful against a
client that completes startup and then never sends anything, which would
otherwise pin a goroutine and a backend connection indefinitely.

Programmatically, use `proxy.WithMaxConnections` and `proxy.WithIdleTimeout`.

#### Metrics

pgproxy always collects counters - total/active connections, ACL and connection-cap rejections, blocked queries,
backend connect errors - for external monitoring (REVIEW.md M8). Set `MetricsLogInterval` (e.g. `"1m"`) to log a
snapshot at INFO level on that interval; leaving it empty just means nothing is logged periodically, not that
nothing is collected.

Programmatically, pass a `*proxy.Metrics` via `proxy.WithMetrics` and read it anytime with `Snapshot()`:

```go
metrics := &proxy.Metrics{}
proxy.Start(addr, dbs, handler, proxy.WithMetrics(metrics))
// later, from any goroutine:
snap := metrics.Snapshot()
log.Printf("active=%d blocked=%d", snap.ActiveConnections, snap.BlockedQueries)
```

#### Reloading the filter (SIGHUP)

Sending pgproxy `SIGHUP` re-reads the config file's `[Filter]` section and hot-swaps it into the running proxy via
`QueryFilter.UpdateConfig`, without dropping the listener or any live session. Only `[Filter]` is reloadable this
way - `[ServerConfig]`, `[ACL]`, and `[DB.*]` need a full restart, since changing them means rebinding the listener
or renegotiating already-open sessions. A reload that fails to parse (bad TOML, missing file) logs an error and
leaves the previous filter config in effect.

```bash
systemctl reload pgproxy   # or: kill -HUP <pid>
```

#### Query cancellation

`psql`'s Ctrl-C and driver-level query cancellation (a `CancelRequest` sent
on a fresh connection) are supported: pgproxy remembers which backend a
session's `BackendKeyData` belongs to and forwards the cancellation to that
same backend.

### Be called as a package

[package_example](https://github.com/bzed/pgproxy/blob/master/examples/package_example/package_example.go)

```
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bzed/pgproxy/cli"
)

func main() {
	// call proxy; empty configPath falls back to the -config flag
	cli.Main("../pgproxy.conf")

	// Capture ctrl-c for graceful exit
	chExit := make(chan os.Signal, 1)
	signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM)
	<-chExit
	fmt.Println("Example EXITING...Bye.")
}
```

Check the [`examples/`](https://github.com/bzed/pgproxy/tree/master/examples) directory for more use cases:
- `client_example`: Basic PostgreSQL connection and querying through pgproxy.
- `package_example`: Standard proxy startup embedded within Go code.
- `multi_db_example`: Example of configuring pgproxy to route clients dynamically to multiple disparate PostgreSQL instances based on the requested database name.
- `unix_socket_example`: Demonstrates how to host pgproxy locally over a Unix Socket for enhanced security, bypassing TCP completely.

#### Per-connection context in a handler

`proxy.Start`'s `Handler` only sees a query's text. For per-user/per-database rules, identity-aware audit logging, or
rate limiting, pass a `proxy.ContextHandler` via `proxy.WithContextHandler` instead: it additionally receives a
`proxy.ConnInfo` (connection id, `user`, requested database, and remote address) with every query.

```go
proxy.Start(addr, dbs, nil, proxy.WithContextHandler(func(info proxy.ConnInfo, query string) ([]byte, error) {
    log.Printf("conn=%d user=%s db=%s addr=%s query=%s", info.ConnID, info.User, info.Database, info.RemoteAddr, query)
    return nil, nil // nil, nil: forward the query unchanged
}))
```

### Systemd Deployment

A ready-to-use systemd service file is provided in `systemd/pgproxy.service`. This file employs modern systemd security recommendations (running as `postgres:postgres`, `ProtectSystem=full`, `PrivateTmp=yes`, `NoNewPrivileges=yes`, etc.) to run pgproxy securely in production.

To deploy:
```bash
sudo cp systemd/pgproxy.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now pgproxy
```

## SQL Support

pgproxy parses and classifies `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `ALTER ROLE`, `SET`/`RESET` and
`EXECUTE` statements for the per-statement-type filter rules below using
[pg_query_go](https://github.com/pganalyze/pg_query_go) - a Go binding of `libpg_query`, which is the actual
PostgreSQL parser (extracted from PostgreSQL's own source), not a reimplementation. In practice this means pgproxy
now parses essentially anything real PostgreSQL accepts, including statement kinds an earlier version of pgproxy
could not parse at all: `COPY`, `LISTEN`/`NOTIFY`, `DELETE ... USING`, `GENERATED ALWAYS AS IDENTITY`, full-text
`@@`, `MERGE`, `VACUUM`, `DO`, `CALL`, cursors, `SET SESSION AUTHORIZATION <value>`, and more.

**Cgo dependency**: `pg_query_go` wraps `libpg_query`'s C implementation via cgo, so `parser/` (and therefore
pgproxy) needs a working C toolchain to build. A native build needs nothing beyond what's normally already present
(cgo is enabled and a compiler auto-detected by default); cross-compiling to Windows needs a mingw-w64 cross
compiler, since `CGO_ENABLED=0` silently drops the parser's exported functions rather than failing loudly. See
`AGENTS.md`'s "Cgo dependency" section for the exact build commands and package names.

**Remaining parser gaps**: even the real grammar can occasionally fail to parse - genuinely malformed input, or
syntax newer than the bundled `libpg_query` version. What happens to a statement pgproxy can't parse is controlled
by `on_parse_error` (see below); this path should now be rare in practice.

**Nested mutations**: the per-statement-type rules apply to a mutation (`INSERT`/`UPDATE`/`DELETE`/`TRUNCATE`)
wherever it appears in the statement, not just at the top level - inside a CTE, a `FROM`/`JOIN` subquery, a scalar
or `EXISTS`/`IN` subquery, an `INSERT ... SELECT` source, an `UPDATE ... SET` expression, or a `CREATE TABLE AS`
source, at any nesting depth. A data-modifying CTE hidden inside a subquery is not a way around `allow_delete`,
`require_where_for_delete`, etc.

**Filter scope**: the per-statement-type rules (`allow_select`, `allow_insert`, `allow_set_var`, `allow_execute`,
...) and the `require_where_for_*` rules only apply to `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`,
`ALTER ROLE`, `SET`/`RESET` and `EXECUTE`. Any other statement type (`CREATE`, `DROP`, `GRANT`, `COPY`, ...) is
**allowed by default** unless it also matches `signature_filter_enabled`'s block/allow list. If you rely on pgproxy
as a firewall against those statement types, use signature-based filtering (or restrict backend-side privileges)
rather than the per-statement-type rules alone.

**SET/RESET**: `allow_set_var` defaults to `true`, since drivers and ORMs routinely send `SET` statements as part
of connecting (pgjdbc's `SET extra_float_digits = 3`, ActiveRecord's `SET client_min_messages`, `SET TIME ZONE`,
...) and refusing them by default would break most clients out of the box. `block_set_vars` (default
`["session_authorization", "role"]`) is always enforced regardless of `allow_set_var`, since those two GUCs let a
session change its own effective privileges mid-connection.

**EXECUTE**: `allow_execute` defaults to `false`. pgproxy filters `Parse`'s query text, but by the time a later
simple-protocol `EXECUTE name` (or extended-protocol `Execute` of a previously `Parse`d/`Bind`-bound statement)
runs, the filter cannot re-inspect what that prepared statement actually does - so it is refused by default.

**Statements the parser can't handle**: `on_parse_error` (default `"block"`) controls what happens when a
statement fails to parse (see "Remaining parser gaps" above). `"allow"` forwards it to the backend **unfiltered -
this bypasses every rule above for any statement the parser can't handle**, and should only be enabled if you
understand and accept that. `"audit"` behaves like `"allow"` but first logs the statement text (with a best-effort
redaction of `PASSWORD`/`IDENTIFIED BY` literals - not a guarantee against every way a secret could appear in a
statement) so you can see what's being let through.

**Signature format changed**: `block_signatures`/`allow_signatures` match against the statement as normalized by
the real PostgreSQL parser (constants replaced with `$1`, `$2`, ... placeholders - e.g.
`SELECT * FROM users WHERE id = $1`), not the previous parser's `(col = _)` style. Signatures written for a
pgproxy version before the parser upgrade must be rewritten to match; the easiest way to get a signature right is
to log it once with `signature_audit_mode = true` and copy the logged value.

**Signature filtering footgun**: if you set `signature_filter_enabled = true` and `signature_allow_by_default = false`
without populating `allow_signatures`, pgproxy will block *every* statement. A blocked statement gets an
`ErrorResponse` and the session keeps running (it does not disconnect the client), so "everything errors instead of
working" is the visible symptom rather than constant disconnects. pgproxy logs a warning at startup when it detects
this combination; treat it as a configuration error.

## Credits

Package parser utilizes [pg_query_go](https://github.com/pganalyze/pg_query_go), a Go binding of PostgreSQL's own
`libpg_query`.
