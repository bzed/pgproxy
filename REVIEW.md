# pgproxy Code Review

Date: 2026-09-06
Scope: full review of `main.go`, `cli/`, `proxy/`, `parser/`, examples, CI, packaging.

How findings were verified:
- `export GOCACHE=/tmp/go-cache && go build ./...` — OK
- `go vet ./...` — OK
- `golangci-lint run --config .golangci.yml ./...` — OK
- `go test -count=1 ./...` — OK (but see T2, proxy package takes ~10s without a DB due to sleeps)
- `go test -race -count=1 ./proxy -run "TestMock|TestProxy|TestPgMock"` — **FAILS** (see C1)
- `GOOS=windows GOARCH=amd64 go build ./...` — OK

Severity levels: Critical (correctness/data corruption), High (broken features/security), Medium (robustness/maintainability), Low (polish).

---

## Critical

### C1. Data race + permanent goroutine deadlock in `Proxy.err` (proxy/proxy.go:132-141)
`p.erred` is read/written from both pipe goroutines without synchronization, and `p.errsig`
is unbuffered while `service()` (proxy/proxy.go:183) receives from it exactly once.

Reproduced today: `go test -race -count=1 ./proxy -run TestProxyWithUnixSocket` fails with
`WARNING: DATA RACE` (read at proxy.go:133 in `handleResponseConnection` vs previous write at
proxy.go:140 in `handleIncomingConnection`).

Worse than the race: when both directions error (the normal case when a connection closes),
the second caller of `p.err` passes the `p.erred` check (it is set only *after* the channel
send) and then blocks forever on `p.errsig <- true`. Every closed connection leaks a goroutine.

Fix: replace channel-send semantics with `sync.Once` + `close(p.errsig)`, or guard with a
mutex and make `errsig` buffered (cap 1).
Acceptance: `go test -race ./proxy/...` passes; a leak test (e.g. `goleak`) on a closed
connection shows no growth.

### C2. Buffer pool double-put causes cross-connection buffer aliasing (proxy/proxy.go:217-218, 266-272)
`defer p.bufPool.Put(buff)` evaluates `buff` when the defer is *scheduled* (the original
64KB buffer). If a message larger than 64KB arrives, line 270 puts that original buffer back
into the pool and reassigns `buff = newBuff`; on return the deferred call puts the *same
original buffer* into the pool a second time. `sync.Pool` now holds one backing array twice,
so two concurrent connections can `Get()` the identical buffer and overwrite each other's
wire data (silent query corruption, wrong routing).

Fix: `defer func() { p.bufPool.Put(buff) }()` so the current value is captured at exit.
Acceptance: unit test that sends a >64KB message through one proxy connection while a second
connection is active, with distinct payloads asserted on the mock backend.

### C3. Extended protocol (Parse/Bind) messages are mangled and then blocked (proxy/proxy.go:207-213, 286-302, 324-348)
`isQueryMessage` includes `ParseMsg` ('P') and `BindMsg` ('B'):
- A Parse message body is `name\0 query\0 int16 paramCount + OIDs` — not a plain SQL string.
- A Bind message body is portal/statement names, format codes and **binary** parameter values.

`HandleQuery` converts the whole body to a string and hands it to the handler. With the
production handler (`parser.QueryFilter.Handler`) the SQL parser fails on the binary blob,
`Filter` returns false, the handler errors, and per H2 the connection is killed. Net effect:
any client using the extended query protocol — lib/pq with parameters (database/sql
`Prepare`), pgx, JDBC, Npgsql — cannot work through pgproxy at all. If a handler ever
*rewrites* the content, the reconstructed message is protocol garbage (dropped OIDs,
corrupted binary params).

Note `bytes.TrimSuffix(content, []byte{0})` only strips one null and may strip a byte that
is part of a trailing OID/value.

Fix: only treat the SQL text of SimpleQuery ('Q') and the query field of Parse ('P') as
query; forward Bind verbatim. Best done as part of C4 by decoding messages with pgproto3.
Acceptance: mock-server test driving Parse/Bind/Execute/Sync through the proxy with a
parameterized query (can be hand-rolled with pgproto3 types, or via pgx).

### C4. Manual wire-protocol parsing violates the project's own hard rule (proxy/proxy.go:216-311, proxy/auth.go:23-77)
AGENTS.md: *"NEVER construct or parse PostgreSQL wire protocol messages manually via byte
buffers (`binary.BigEndian`, `bytes.Buffer`). ALL protocol interactions MUST natively use
`github.com/jackc/pgproto3/v2`"*. Both `handleIncomingConnection` and `readStartupMessage`
hand-roll framing with `io.ReadFull`/`binary.BigEndian`; the mock server in
`proxy_mock_test.go` does the same. This is the root cause of C3 and H5 and blocks proper
handling of anything beyond Q/P/B. Refactor to `pgproto3.Frontend`/`pgproto3.Backend`
(message-level decode/re-encode, keeping pass-through for untouched types).

---

## High

### H1. `DBConfig.DBName` is never used; backend receives the client's database name (proxy/auth.go:113-117, proxy/proxy.go:158-168)
Routing matches the client's `database` startup parameter against the config *key*
(`master`, `local_socket`, ...), then forwards the client's original StartupMessage
verbatim. The backend therefore sees `database=master` or `database=local_socket`, which
usually does not exist there. The documented config `[DB.local_socket] DBName="postgres"`
(proxy.conf:20-22, README) is unreachable. Fix: rebuild the startup message with
`database=dbConf.DBName` (pgproto3.StartupMessage makes this trivial after C4), or drop
DBName and document that the key must equal a real backend database name.

### H2. A blocked query kills the client connection instead of returning an SQL error (proxy/proxy.go:288-292, parser/filter.go:147-152)
When the handler returns an error, `p.err(...)` tears the session down with no
ErrorResponse — the client sees an unexplained dropped connection mid-session. Fix: write a
`pgproto3.ErrorResponse` (severity FATAL or ERROR) to the client; for ERROR also consider a
ReadyForQuery so the session can continue. Combined with C3 and M8, a single parameterized
query or aggressive signature config severs every connection.

### H3. Backend TLS with `InsecureSkipVerify: true` (proxy/auth.go:109-111)
If the backend answers SSLRequest with 'S', the proxy upgrades with no certificate or
hostname verification — a MITM between proxy and backend is undetected, while the proxy
itself tells the client encryption was refused. Add TLS config (CA, server name, verify
mode) to DBConfig; default to verify when a CA is provided.

### H4. CancelRequest is dropped (proxy/auth.go:44-47, proxy/proxy.go:153-156)
`readStartupMessage` returns `nil` params for CancelRequest and `service` aborts, so query
cancellation (psql Ctrl-C, driver context deadlines) never reaches the backend — statements
keep running. Either forward the CancelRequest to the backend that owns the target PID/secret
(needs backend-key bookkeeping per connection), or at minimum document the limitation.
GSSENCRequest (code 80877104) is also unhandled and currently yields "unknown startup code".

---

## Medium

### M1. go.mod is not tidy
`github.com/coreos/go-systemd/v22` is marked `// indirect` (go.mod:23) but directly
imported by `proxy`. gopls flags it. Run `go mod tidy`.

### M2. Dead CLI code (cli/cmd.go, cli/cmd_test.go)
`Command()`/`Client` have no caller (the `pgproxy cli` subcommand was removed in commit
cc3c728). Issues while it exists: deprecated `reader.ReadLine()`, pointless deferred `err`
check (always nil after successful Open), `rows` never closed in `Request` (cli/cmd.go:77-82),
`time.Sleep(300000 * time.Nanosecond)`, `glog.Fatalln` reachable from `TestCommand`.
Removing it also drops the sqlx/lib/pq/tablewriter dependencies from the daemon binary.

### M3. Dead code in main.go (main.go:17-53)
`loggingHandler` is kept alive only by `_ = loggingHandler` (main.go:18); its metadata
result is discarded into `_`. It drags the whole postgresql-parser into package main for no
function. Remove (the filter lives in `parser/`), or wire it in deliberately.

### M4. Documentation / unit-file drift
- README documents `User`/`Password` keys per `[DB.*]` (README.md:53-66); `DBConfig`
  (proxy/auth.go:16-19) has only `Addr`/`DBName` — TOML silently ignores them, so credentials
  in the README config do nothing.
- README "Using" section advertises `pgproxy start/stop` and `pgproxy cli` subcommands that
  no longer exist; `systemd/pgproxy.service` ExecStart uses the removed `start` subcommand
  and a nonexistent `-c` flag (`-config` is the real flag; see debian/pgproxy.service).
- README example passes `[]string{"pgproxy","start"}` as `pargs` to `cli.Main`, which
  ignores the parameter entirely (also see M10).

### M5. `readConfig` address parsing is fragile (cli/utils.go:59-67)
`strings.Index(master.Addr, ":")` breaks for IPv6 (`[::1]:5432`) and unix-socket masters;
use `net.SplitHostPort`. Also mixes `os.Exit(int(syscall.ENOENT))` and `glog.Fatalln` — a
library package should return errors to the caller.

### M6. `RowsFormater` output bugs (proxy/formate.go:37, 62-64, 85-96)
`data := make([][]string, 1)` seeds an empty first row that is always appended (blank row in
every rendered table); `rows.Err()` is never checked; `interface2String` renders int,
float, bool, time and NULL as `""`. formate_test.go:29-36 even cements the panic-on-nil
behavior. Moot if M2 removes the CLI, otherwise fix.

### M7. Test hygiene in proxy/proxy_test.go
- Fixed ports 9090/9091/9092: collide with parallel runs / leftover processes; `getListener`
  failure calls `glog.Fatalf`, killing the whole test binary. Prefer `net.Listen("tcp", ":0")`
  plus an address-passing helper, mirroring the mock server's pattern.
- `time.Sleep(3s/5s)` runs before the DB-availability skip (proxy/proxy.go tests), so
  DB-less environments still pay ~10s per package run. Probe the backend first, skip early.
- CI go-test.yml:41 invokes `go test` with a hand-maintained list of `.go` files
  (`./proxy/proxy_unit_test.go ./proxy/proxy.go ...`) — brittle; use package + `-run` filters.

### M8. Signature-filter defaults can DoS the proxy (parser/filter.go:72-108, pgproxy.conf:40-46)
With `signature_filter_enabled=true` and `signature_allow_by_default=false` (the shipped
comment example) and an empty `allow_signatures`, *every* statement is blocked — and via H2
that means every client connection is killed. There is no startup warning about this
combination. Either treat "allow list empty + allow_by_default=false" as a config error, or
log a loud warning at startup.

### M9. Stale CI configuration
`.travis.yml` tests Go 1.6-1.8 and duplicates codecov upload already covered by
`.github/workflows/go-test.yml`. Delete it.

### M10. No graceful shutdown anywhere
`Start()` loops forever (proxy/proxy.go:46-63); `cli.Main` exits on SIGTERM without closing
the listener, draining connections, or `SdNotify`-ing `STOPPING=1`. systemd `Type=notify`
(debian unit) plus no stopping notification means restarts look like crashes. Thread a
context or a shutdown channel through Start, close the listener, close active proxies.

---

## Low

### L1. `connid` global (proxy/proxy.go:24-26, 52)
Only incremented in the accept loop today, but a bare global invites races once multiple
listeners exist; `atomic.Uint64` is cheap. `%03d` prefix also stops aligning after 999
connections.

### L2. glog output invisible under systemd by default
glog defaults to writing files under /tmp; neither `systemd/pgproxy.service` nor
`debian/pgproxy.service` pass `-logtostderr`, so `journalctl` shows nothing. Pass the flag in
the units or set `flag.Set("logtostderr", "true")` in `cli.Main`.

### L3. Systemd socket activation details (proxy/proxy.go:67-86)
`listeners[0]` silently ignores any additional activated sockets, and when activation
provides a listener the configured `ProxyAddr` is ignored without a log line. Log both cases.

### L4. Filter scope is undocumented (parser/filter.go:110-141)
Statements not covered by the switch (CREATE, DROP, GRANT, COPY, SET, ...) are allowed by
default. That may be intended, but it should be stated in README/AGENTS since the feature is
marketed as a firewall.

### L5. `cli.Main(config interface{}, pargs interface{})` (cli/cli.go:29)
Both parameters are `interface{}` used only as an optional string; `pargs` is never used.
Change to `Main(configPath string)` (empty = flag default) and update callers/examples.

### L6. Startup length cap 10000 (proxy/auth.go:30)
Legitimate startup packets with many/wide parameters can exceed 10KB and are rejected as
invalid. Resolves itself with C4 (pgproto3 has sane limits).

---

## What is in good shape
- Layered tests: pure unit (proxy_unit_test.go, parser, cli, main), mock-server integration
  without PostgreSQL, pgmock-based tests, DB-gated integration tests with skip logic.
- golangci-lint, go vet, gofmt, Windows cross-build all clean.
- Debian packaging with hardened unit files and socket activation support.

## Suggested fix order
1. C1, C2 (concurrency correctness — small, isolated fixes)
2. C4 + C3 (migrate to pgproto3; fixes extended protocol)
3. H2, H1, H4 (client-facing behavior), H3 (TLS config)
4. M1-M4 (hygiene, docs), then the rest
