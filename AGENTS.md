# pgproxy - Agent Requirements

## Language & Framework
- **Primary Language**: Go (Golang)
- **Go Version**: 1.25+ (project uses go 1.26 in go.mod)

## Development Requirements

### Code Quality
- **Always run `go vet ./...`** before committing to catch potential issues
- **Always run `gofmt -l .`** and format any unformatted files with `gofmt -w`
- **Never commit unformatted code** - the CI pipeline will reject it
- Use `golangci-lint` for additional static analysis (config in `.golangci.yml`)

### Testing
- Run unit tests: `go test -v ./parser/... ./cli/...`
- Run mock server tests (no PostgreSQL required): `go test -v -run "TestMock|TestProxy|TestPgMock" ./proxy`
- Integration tests require PostgreSQL and run only on Linux in CI
- New features must include corresponding tests

### Build
- **Always ensure `go build ./...` succeeds** before committing
- **Always ensure `go vet ./...` and `gofmt -l .`  succeed** before committing
- The project builds for Linux, macOS, and Windows

### Code Style
- Follow standard Go conventions (Effective Go)
- Use descriptive variable and function names
- Add comments for exported types and functions
- Keep lines under 120 characters where practical
- Use `goimports` for consistent import formatting

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

### CI/CD
- GitHub Actions workflows are in `.github/workflows/`
- All PRs must pass all CI checks before merging
- Mock tests run on all platforms
- Integration tests run on Linux only (require PostgreSQL container)

### PostgreSQL Integration
- Integration tests connect to localhost:5432
- Credentials: user=postgres, password=testpass, dbname=testdb
- Use unique ports for test proxies (9090, 9091, 9092, etc.) to avoid conflicts
