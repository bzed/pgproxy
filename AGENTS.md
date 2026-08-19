# pgproxy - Agent Requirements

## Language & Framework
- **Primary Language**: Go (Golang)
- **Go Version**: 1.25+ (project uses go 1.26 in go.mod)

## Development Requirements

### Build Environment Constraints
- **CRITICAL**: The default build cache is read-only. You **MUST** set `export GOCACHE=/tmp/go-cache` for all `go` commands (build, run, test, vet).
- **CRITICAL**: You **MUST** set `export GOLANGCI_LINT_CACHE=/tmp/golangci-lint-cache` for all `golangci-lint` runs.

### Code Quality
- **Always run `export GOCACHE=/tmp/go-cache && go vet ./...`** before committing to catch potential issues
- **Always run `gofmt -l .`** and format any unformatted files with `gofmt -w`
- **Always run `export GOCACHE=/tmp/go-cache && export GOLANGCI_LINT_CACHE=/tmp/golangci-lint-cache && golangci-lint run --config .golangci.yml ./...`** for additional static analysis before committing
- **Never commit unformatted code** - the CI pipeline will reject it
- **Never commit code that fails golangci-lint** - the CI pipeline will reject it

### Testing
- Run all unit and mock tests: `export GOCACHE=/tmp/go-cache && go test -v ./...`
- Mock server tests and protocol logic reside in `./proxy/...`
- New features must include corresponding tests
- Unix socket testing is automatically skipped on Windows via build flags / runtime checks.

### Build
- **Always ensure `go build ./...` succeeds** before committing
- The project builds for Linux, macOS, and Windows

### Code Style
- Follow standard Go conventions (Effective Go)
- Use descriptive variable and function names
- Add comments for exported types and functions
- Keep lines under 120 characters where practical

### Git
- **Never rewrite commits** (no `git commit --amend`, no `git rebase -i`)
- Each commit should be a single logical change
- Write clear, descriptive commit messages
- Reference issues/PRs when applicable

### Platform Compatibility
- Code must build and test on Linux, macOS, and Windows
- Avoid platform-specific code (e.g., `syscall.Kill` - use cross-platform alternatives like `os.Process.Kill`)
- Test Windows compatibility with `GOOS=windows GOARCH=amd64 go build ./...`

### Dependencies
- Use `go mod tidy` to update go.mod and go.sum
- Keep dependencies updated
- Prefer well-maintained, popular libraries

### PostgreSQL Integration
- **PostgreSQL Wire Protocol**: NEVER construct or parse PostgreSQL wire protocol messages manually via byte buffers (`binary.BigEndian`, `bytes.Buffer`). ALL protocol interactions MUST natively use the robust `github.com/jackc/pgproto3/v2` library (`pgproto3.Frontend`, `pgproto3.Backend`, etc.) to prevent packet framing errors.
- Integration tests connect to localhost:5432
- Credentials: user=postgres, password=testpass, dbname=testdb
- Use unique ports for test proxies (9090, 9091, 9092, etc.) to avoid conflicts
