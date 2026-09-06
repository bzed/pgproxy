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
* Native PostgreSQL parsing engine (based on CockroachDB parser)

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

    # block_signatures = ["SELECT * FROM users WHERE (id = _) AND (name = _)"]
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

### Systemd Deployment

A ready-to-use systemd service file is provided in `systemd/pgproxy.service`. This file employs modern systemd security recommendations (running as `postgres:postgres`, `ProtectSystem=full`, `PrivateTmp=yes`, `NoNewPrivileges=yes`, etc.) to run pgproxy securely in production.

To deploy:
```bash
sudo cp systemd/pgproxy.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now pgproxy
```

## SQL Support

pgproxy uses [postgresql-parser](https://github.com/auxten/postgresql-parser), a SQL parser derived from
CockroachDB's, to parse and classify `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `ALTER ROLE`, `SET`/`RESET`
and `EXECUTE` statements for the per-statement-type filter rules below.

**Known parser gaps**: this parser does not implement every PostgreSQL statement. `CREATE FUNCTION`,
`COPY ... TO/FROM STDOUT/STDIN`, `LISTEN`/`NOTIFY`, `DELETE ... USING`, `GENERATED ALWAYS AS IDENTITY`, full-text
`@@`, `MERGE`, `VACUUM`, `DO`, `CALL`, cursors, and `SET SESSION AUTHORIZATION <value>` (as opposed to `DEFAULT`)
are all known to fail to parse today. What happens to a statement pgproxy can't parse is controlled by
`on_parse_error` (see below) - by default such statements are simply refused, which also means pgproxy currently
cannot proxy client sessions that need any of the syntax above.

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
statement fails to parse (see the known gaps above). `"allow"` forwards it to the backend **unfiltered - this
bypasses every rule above for any statement matching a parser gap**, and should only be enabled if you understand
and accept that. `"audit"` behaves like `"allow"` but first logs the statement text (with a best-effort redaction
of `PASSWORD`/`IDENTIFIED BY` literals - not a guarantee against every way a secret could appear in a statement)
so you can see what's being let through.

**Signature filtering footgun**: if you set `signature_filter_enabled = true` and `signature_allow_by_default = false`
without populating `allow_signatures`, pgproxy will block *every* statement. A blocked statement gets an
`ErrorResponse` and the session keeps running (it does not disconnect the client), so "everything errors instead of
working" is the visible symptom rather than constant disconnects. pgproxy logs a warning at startup when it detects
this combination; treat it as a configuration error.

## Credits

Package parser utilizes [postgresql-parser](https://github.com/auxten/postgresql-parser) (derived from CockroachDB's parsing engine).
