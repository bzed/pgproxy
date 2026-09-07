# pgproxy Code Review — 5th pass

Date: 2026-09-07
Scope: fix every item the 4th-pass review left open (C2, H3, H4, M7, M8, and
the remainder of M3/M4). Every "fixed" row below has a regression test
backing it, not just a code change, and every claim about build/test status
was re-verified in this pass, not carried over from memory.

## How findings were verified

- `go build ./...` — OK
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc go build ./...` — OK
  (see C2: this now needs a mingw-w64 cross compiler; verified by actually
  installing one - `gcc-mingw-w64-x86-64-posix` extracted without root into a
  local prefix - and cross-compiling, not just reasoning about it)
- `go vet ./...` — OK
- `golangci-lint run --config .golangci.yml ./...` — OK (exit 0)
- `gofmt -l .` — no output (clean)
- `go test -race -count=1 ./...` — OK, all packages pass, re-run 3x to check
  for flakiness (one genuine flake was found and fixed - see M8 row/notes)
- Per-package coverage: main 100%, cli 91.1%, parser 91.0%, proxy 92.9% — 85%
  gate met everywhere

### Status of the 4th-pass findings

| Old ID | Status | Notes |
|---|---|---|
| C2 (CockroachDB-derived parser; `on_parse_error=allow` bypass) | **fixed** | Parser replaced: `parser/filter.go` now uses `github.com/pganalyze/pg_query_go/v6`, a Go binding of `libpg_query` - PostgreSQL's *actual* parser, not a reimplementation. Every previously-failing statement family (`COPY`, `LISTEN`, `DELETE...USING`, `MERGE`, `VACUUM`, `DO`, `CALL`, cursors, `GENERATED ALWAYS AS IDENTITY`, full-text `@@`, `SET SESSION AUTHORIZATION <value>`) now parses; `TestFilter_RealPostgreSQLGrammarParses` covers all of them. **Trade-off, disclosed and accepted by the user**: `pg_query_go` uses cgo, so it is not pure Go as the 3rd-pass review assumed. This means (a) `CGO_ENABLED=0` silently drops the parser's functions rather than failing loudly, and (b) cross-compiling to Windows needs a mingw-w64 toolchain - both now documented in AGENTS.md's new "Cgo dependency" section and README's "SQL Support". `extractStatements`' reflection walk (C1, kept from the 4th pass) was ported to walk `pgquery.Node`'s protobuf oneof instead of the old `tree.Statement` interface - same generic-walk approach, new node shape. Signature-based filtering's format changed (real `$1`/`$2` placeholders via `pgquery.Normalize`, not the old `(col = _)` style) - a breaking change for anyone with existing `block_signatures`/`allow_signatures`, called out loudly in README. |
| H3 (frontend TLS) | **fixed** | `readStartupMessage` now accepts an optional `*tls.Config`: with one configured (`proxy.WithTLS`, built via the new `proxy.NewFrontendTLSConfig(cert, key, clientCA)`), SSLRequest gets `'S'` and the connection is upgraded (with optional mutual TLS via `clientCA`) instead of always `'N'`. `TestReadStartupMessage/SSLRequest_is_accepted...` and `TestProxyWithFrontendTLS` (full session through a live proxy) cover it. Wired into `cli`'s TOML config as `[ServerConfig] TLSCert/TLSKey/TLSClientCA`. |
| H4 (no proxy-level ACLs) | **fixed** | New `proxy.ACL` (`AllowedCIDRs`/`AllowedUsers`/`AllowedDatabases`, each an allowlist) checked via `proxy.WithACL`: source address at accept time, user/database after the StartupMessage is parsed. A rejected session gets a FATAL `ErrorResponse` (`28000` for address, `42501` for user/database) and is closed. `TestProxyACL` covers all three checks plus the all-pass case. Wired into `cli`'s TOML config as `[ACL]`. |
| M3 (remaining e2e gaps) | **partial** | Added: frontend-TLS session, ACL (all four cases), max-connections (including slot reuse after a session closes), idle-timeout, context-handler, and metrics (connections/rejections/blocked/backend-errors) end-to-end tests, all through a live `Start()` + mock backend. Still missing, as before: extended-protocol (Parse/Bind/Execute/Sync) round trip, `FunctionCall`/`NegotiateProtocolVersion`/COPY/replication-mode sessions. |
| M4 (no connection limits/timeouts) | **fixed** | `proxy.WithMaxConnections` caps concurrent sessions (a rejection gets SQLSTATE `53300`, matching real PostgreSQL); `proxy.WithIdleTimeout` closes a session that goes too long between client messages (`SetReadDeadline`, reset per message). `TestProxyMaxConnections` and `TestProxyIdleTimeout` cover both, including that a closed session's slot is reusable. Wired into `cli` as `[ServerConfig] MaxConnections/IdleTimeout`. (The dial-timeout half was already fixed in the 4th pass.) |
| M7 (Handler API has no context) | **fixed** | New `proxy.ContextHandler func(ConnInfo, string) ([]byte, error)` (set via `proxy.WithContextHandler`) receives `ConnInfo{ConnID, User, Database, RemoteAddr}` alongside every query - takes priority over the plain `Handler` when both are set. `TestProxyContextHandler` verifies every field is populated correctly through a real session. Not wired into `cli`'s TOML config (the shipped `QueryFilter.Handler` doesn't need per-connection context), but available to any program using pgproxy as a library - documented in README under "Per-connection context in a handler". |
| M8 (no metrics, no query log, no SIGHUP reload) | **fixed** | **Metrics**: `proxy.Metrics` (set via `proxy.WithMetrics`) counts total/active connections, ACL/max-connections rejections, blocked queries, and backend connect errors; nil-safe throughout so code that builds a `*Proxy` directly (unit tests) never needs to care. `TestProxyMetrics` verifies every counter against real traffic. Wired into `cli` unconditionally (always collected) with optional periodic INFO-level logging via `[ServerConfig] MetricsLogInterval`. **SIGHUP reload**: `parser.QueryFilter` now stores its config behind an `atomic.Pointer` with a new `UpdateConfig` method (safe to call concurrently with `Filter` - verified under `-race`); `cli.run` re-reads `[Filter]` and calls `UpdateConfig` on SIGHUP, without dropping the listener or any session. Only `[Filter]` reloads this way - `[ServerConfig]`/`[ACL]`/`[DB.*]` still need a restart, documented as such. Systemd unit files gained `ExecReload=/bin/kill -HUP $MAINPID` so `systemctl reload pgproxy` actually works. **Query log**: not added as a separate feature - `ContextHandler` (M7) is the hook for it; a query log is a few lines inside one, and shipping pgproxy's own opinionated log format/rotation was judged lower value than the hook itself. |
| C1, H1, H2, M1, M2, M5, M6, M9, L1-L4 | **unchanged, still fixed** | Carried over from the 4th pass; re-verified as part of this pass's full regression run, not re-litigated. |

Note on verification process: `TestProxyMaxConnections`/`TestProxyMetrics` each
hit a genuine, reproducible race the first time they were written - both
traced to a shared test helper (`waitForListener`) making its own probe
connection, which legitimately (and correctly) counts against
`WithMaxConnections`'/metrics' totals just like a real client's connection
would. Both tests were fixed by baselining against/waiting past that probe
rather than assuming a connection count of zero; this is called out in case
it recurs in a future test using the same helper alongside either feature.

---

## What's left (unchanged from the 4th pass, not attempted here either)

Nothing from the 4th-pass "still open" list remains except the parts of
M3/M4 noted above (extended-protocol/COPY/replication test coverage). Every
Critical/High finding across all five passes is now fixed.

## What is in good shape

- The real PostgreSQL grammar (C2) removes an entire class of prior
  findings at the root: every "parser can't handle X" gap from the 1st
  through 4th passes is gone, not papered over.
- `extractStatements`' generic-reflection-walk design (introduced for C1)
  ported cleanly to the new parser's protobuf node shape with no loss of
  the "finds a bypass regardless of nesting" property - a sign the original
  design was sound, not parser-specific.
- H3/H4/M4/M7/M8 all landed through one consistent `proxy.Option`
  mechanism (`WithTLS`, `WithACL`, `WithMaxConnections`, `WithIdleTimeout`,
  `WithContextHandler`, `WithMetrics`), added without breaking any of the
  ~40 existing `proxy.Start(addr, dbs, handler)` call sites across the test
  suite and examples.
- Every new feature is wired into the TOML config `cli` actually reads, not
  left as library-only capabilities nobody using the shipped binary can
  reach (the one deliberate exception, `ContextHandler`, is documented as
  such).
- Metrics/ACL/TLS/connection-limit code is nil-safe by construction
  (`*Metrics`, `*compiledACL` methods all handle a nil receiver), so a
  `*Proxy` built directly - as most of the existing unit tests do, bypassing
  `Start` - never needed to be touched to stay panic-free.
