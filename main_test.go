package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bzed/pgproxy/cli"
)

// Test_main_versionFlag is a smoke test (REVIEW.md L4): main.go itself had
// no tests at all. This drives the actual main() entrypoint - not just
// cli.Main, which is already covered in the cli package - with `-version`,
// which prints the version and returns immediately instead of blocking on
// a shutdown signal or requiring a config file, making it the one main()
// path that's actually practical to exercise as a unit test.
func Test_main_versionFlag(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"pgproxy", "-version"}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	main()

	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("failed to read captured stdout: %v", err)
	}

	if got := strings.TrimSpace(buf.String()); got != cli.VERSION {
		t.Errorf("main() with -version printed %q, want %q", got, cli.VERSION)
	}
}
