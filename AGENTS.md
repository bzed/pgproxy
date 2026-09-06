# pgproxy - Agent Requirements

## Project Overview

pgproxy is a PostgreSQL wire-protocol proxy with SQL filtering. Packages:
`proxy/` (connection handling, wire protocol relay), `parser/` (SQL filter rules),
`cli/` (config + entrypoint), `main.go`. Open findings and prioritized work items
live in `REVIEW.md` — read it before changing `proxy/` or `parser/`.

## Language & Framework

- **Primary Language**: Go
- **Go Version**: 1.25+ (go.mod declares 1.26)

## Build Environment Constraints

- **CRITICAL**: The default build cache is read-only. You **MUST** set `export GOCACHE=/tmp/go-cache` for every `go` command (build, run, test, vet, cover).
- **CRITICAL**: You **MUST** set `export GOLANGCI_LINT_CACHE=/tmp/golangci-lint-cache` for all `golangci-lint` runs.

## Standard Commands

```bash
# Build (must succeed before any commit)
export GOCACHE=/tmp/go-cache
go build ./...

# Vet + lint + format (all must be clean before committing)
go vet ./...
gofmt -l .                              # must print nothing; fix with gofmt -w
export GOLANGCI_LINT_CACHE=/tmp/golangci-lint-cache
golangci-lint run --config .golangci.yml ./...

# Tests
go test ./...                           # full suite
go test -race -count=1 ./...            # race detector; must pass
go test -coverprofile=/tmp/cover.out ./... && go tool cover -func=/tmp/cover.out
                                        # coverage gate, see Testing section

# Cross-platform build check
GOOS=windows GOARCH=amd64 go build ./...
```

## Code Quality (clean Go)

- Follow [Effective Go](https://go.dev/doc/effective_go) and the prevailing style
  in the file you are editing; match existing naming, error wrapping, and comment
  conventions before introducing new ones.
- **Errors**: always check returned errors; never discard with `_` unless the
  intent is obvious and commented. Wrap with context using `fmt.Errorf("...: %w", err)`.
  Do not log AND return the same error — pick one (return up, log at the boundary).
- **Naming**: descriptive, idiomatic (`recvBuf`, not `b2`; `userID`, not `userId`).
  Exported identifiers need doc comments starting with the identifier name;
  comments explain *why*, not *what*.
- **Keep it flat and small**: functions do one thing; prefer early returns over
  nested conditionals; no `else` after `return`. Avoid clever one-liners.
- **Structs and APIs**: accept interfaces/parameters you actually need; return
  concrete types; no `interface{}`/`any` where a concrete type works. New exported
  types/functions in `proxy`/`parser` are de-facto public API — document them and
  keep signatures extensible (context first, options struct for >3 params).
- **Concurrency**: every shared field must have a documented synchronization
  strategy (mutex, channel, atomic). Goroutines must always have an exit path —
  no goroutine may outlive its owner without a comment explaining why. Use
  `context.Context` or done-channels for cancellation; never `time.Sleep` for
  synchronization.
- **Resource hygiene**: every `Close`/`Stop`/listener/conn acquired in a function
  is released via `defer` at the point of acquisition. Deferred closures capture
  loop/variable state correctly (`defer func() { f(x) }()` when `x` can change).
- **Constants over magic values**; group related constants in a `const` block.
- Line length limit is **120 characters** (enforced by `lll` linter).
- No dead code, no commented-out code, no `TODO` without an issue reference.

## Testing

- **Coverage requirement: at least 85% per package** (`go tool cover -func`
  "total" line). Measure with the command in Standard Commands before finishing.
  `examples/` is excluded from coverage accounting.
- New features and bug fixes **must include tests** that fail without the change.
- Write **table-driven tests** (`tests := []struct{ name string; ... }{...}` with
  `t.Run` subtests). Test error paths and edge cases, not just the happy path.
- Network tests **must use ephemeral ports**: `net.Listen("tcp", "127.0.0.1:0")`,
  never fixed ports (9090, 29092, ...) — fixed ports make parallel CI runs flaky.
- Synchronize with channels/`require.Eventually`-style polling, not bare
  `time.Sleep`. Keep sleeps under ~100ms where unavoidable.
- Tests that need a real PostgreSQL (localhost:5432, user=postgres,
  password=testpass, dbname=testdb) must probe the server first and
  `t.Skip` cleanly when it is unavailable.
- Unix-socket tests skip on Windows (build tags or runtime `runtime.GOOS` check).
- Protocol-level tests belong in `./proxy` (mock server / pgmock based).
- Run `go test -race ./...` before committing; data races are release blockers.

## Secure Go Development

pgproxy sits on the trust boundary between untrusted clients and database
backends, and handles authentication material. Treat all client input as
hostile.

- **Fail closed**: on parse/decode/filter errors, block the query and return a
  proper ErrorResponse — never fail open, never forward a query you could not
  analyze. A blocked query must never reach the backend.
- **Never log secrets**: passwords, SASL tokens/scram evidence, GSS tokens,
  `BackendKeyData` secret keys, connection strings with credentials, or full
  query bodies that may embed secrets. Log identifiers (connID, user, database)
  instead.
- **TLS**: `InsecureSkipVerify` is only acceptable when the operator explicitly
  configured no-CA mode, must carry a `//nolint:gosec` comment explaining why,
  and must be documented as "encrypted but unverified". Any new TLS path needs
  CA/hostname verification support.
- **Error messages**: never echo raw client input, backend errors containing
  file paths, or internal state into client-visible ErrorResponses beyond what
  the user needs ("query blocked by filter", not stack traces or SQL).
- **Resource limits**: any read path that allocates based on a wire-supplied
  length must be bounded (max message size, read deadlines, max connections,
  dial timeouts). Unbounded `Read`/allocation from untrusted peers is a DoS.
- **No SQL string assembly**: the filter/rewrite layer must not build SQL via
  `fmt.Sprintf` or concatenation of user-controlled input. Rewrites operate on
  parsed statements, not string surgery.
- **Randomness**: secrets/keys/nonces use `crypto/rand`, never `math/rand`.
  Secret comparisons are constant-time (`crypto/subtle`), never `==` on derived
  values.
- **File handling**: config parsing must use exact paths; no path composition
  from client-controlled input. Cert/key files read with `os.ReadFile`, errors
  must not include file contents.
- **Dependencies**: keep the dependency tree minimal; run
  `export GOCACHE=/tmp/go-cache && go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...`
  when adding or upgrading dependencies and resolve any findings. Pin versions;
  never add a dependency to avoid writing a small amount of code.
- **Dangerous APIs**: no `os/exec` from request paths, no `syscall` on
  cross-platform code, no `unsafe`, no world-writable files/sockets (set
  explicit 0600/0770 modes on unix sockets and key files).
- **Registry/state keyed by untrusted data** (e.g. CancelRequest PID/secret):
  always verify the secret before acting, and key by the full tuple
  (backend target + PID) to avoid collisions.

## PostgreSQL Wire Protocol

- **NEVER construct or parse wire-protocol messages manually** via byte buffers
  (`binary.BigEndian`, `bytes.Buffer`, `io.ReadFull` framing). ALL protocol
  interactions MUST use the pgproto3 library (`pgproto3.Frontend`,
  `pgproto3.Backend`), currently `github.com/jackc/pgproto3/v2`
  (`github.com/jackc/pgx/v5/pgproto3` is the maintained successor — see
  REVIEW.md H3/H4 before adding new message handling).
- Pass messages through by decode + re-encode; never modify binary payloads
  (Bind parameters, OIDs) in transit.
- When adding handling for a new message type, add a mock-server test for it
  (the mock currently covers only 'Q'/'X' — see REVIEW.md M10).
- Integration tests connect to localhost:5432 (user=postgres, password=testpass,
  dbname=testdb) and must skip when unavailable.

## Platform Compatibility

- Code must build and test on Linux, macOS, and Windows.
- Avoid platform-specific code (e.g. `syscall.Kill` — use `os.Process.Kill`).
  Unavoidable platform differences go in `*_windows.go`/`*_unix.go` files with
  build tags.
- Verify with `GOOS=windows GOARCH=amd64 go build ./...` before committing.

## Dependencies

- `go mod tidy` after any dependency change; go.mod and go.sum are always
  consistent.
- Prefer well-maintained, popular libraries; check license compatibility
  (project is Apache-2.0).

## Git

- **Never rewrite published commits** (no `git commit --amend`, no `git rebase -i`).
- Each commit is a single logical change with a clear, descriptive message;
  reference issues/PRs where applicable.
- Never commit: unformatted code, lint failures, commented-out code, secrets,
  certificates, or binaries.

## Definition of Done (check before handing off)

1. `go build ./...` succeeds (plus `GOOS=windows` cross-build).
2. `go vet ./...`, `gofmt -l .`, and `golangci-lint run --config .golangci.yml ./...` are clean.
3. `go test -race -count=1 ./...` passes.
4. Package coverage ≥ 85% (`go tool cover -func`).
5. New/changed behavior has failing-first tests; network tests use ephemeral ports.
6. No secrets in logs; error messages safe for client exposure; fail-closed paths verified.
7. Docs (README/REVIEW.md) updated if behavior, config surface, or known gaps changed.
