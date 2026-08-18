package proxy

import (
	"net"
	"testing"
)

var (
	testProxyHost  = "127.0.0.1:9090"
	testRemoteHost = "127.0.0.1:5432"
)

func Benchmark_Start(b *testing.B) {
	// These benchmarks require a running PostgreSQL instance
	// Skipping in CI/automated environments
	b.Skip("Requires running PostgreSQL instance - skipping in automated tests")
}

func Test_Start(t *testing.T) {
	// This integration test requires a running PostgreSQL instance
	// Skipping in CI/automated environments
	t.Skip("Requires running PostgreSQL instance - skipping in automated tests")
}

func Test_getResolvedAddresses(t *testing.T) {
	getResolvedAddresses("127.0.0.1:9090")
}

func Test_getListener(t *testing.T) {
	paddr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	getListener(paddr)
}
