package proxy

import (
	"net"
	"testing"
)

// TestCompileACL covers compileACL's CIDR parsing, including the bare-IP
// (no /mask) acceptance path and the invalid-entry error path, plus the nil
// ACL passthrough compileACL(nil) relies on when WithACL is never used.
func TestCompileACL(t *testing.T) {
	t.Run("nil ACL compiles to nil", func(t *testing.T) {
		c, err := compileACL(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c != nil {
			t.Errorf("compileACL(nil) = %+v, want nil", c)
		}
	})

	t.Run("a bare IPv4 address is accepted as a /32", func(t *testing.T) {
		c, err := compileACL(&ACL{AllowedCIDRs: []string{"192.0.2.7"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := c.checkAddr(&net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 5432}); err != nil {
			t.Errorf("checkAddr(192.0.2.7) = %v, want nil (exact match)", err)
		}
		if err := c.checkAddr(&net.TCPAddr{IP: net.ParseIP("192.0.2.8"), Port: 5432}); err == nil {
			t.Error("checkAddr(192.0.2.8) = nil, want an error (not the exact /32 address)")
		}
	})

	t.Run("a bare IPv6 address is accepted as a /128", func(t *testing.T) {
		c, err := compileACL(&ACL{AllowedCIDRs: []string{"2001:db8::1"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := c.checkAddr(&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 5432}); err != nil {
			t.Errorf("checkAddr(2001:db8::1) = %v, want nil (exact match)", err)
		}
	})

	t.Run("an invalid CIDR entry is a configuration error", func(t *testing.T) {
		if _, err := compileACL(&ACL{AllowedCIDRs: []string{"not-an-address"}}); err == nil {
			t.Error("expected an error for an invalid AllowedCIDRs entry")
		}
	})

	t.Run("a unix socket peer (no IP) is not restricted by CIDR", func(t *testing.T) {
		c, err := compileACL(&ACL{AllowedCIDRs: []string{"10.0.0.0/8"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := c.checkAddr(&net.UnixAddr{Name: "/tmp/.s.PGSQL.5432", Net: "unix"}); err != nil {
			t.Errorf("checkAddr(unix socket) = %v, want nil (CIDR ACLs are TCP-only)", err)
		}
	})

	t.Run("checkUser and checkDatabase on a nil *compiledACL always pass", func(t *testing.T) {
		var c *compiledACL
		if err := c.checkAddr(&net.TCPAddr{}); err != nil {
			t.Errorf("checkAddr on nil = %v, want nil", err)
		}
		if err := c.checkUser("anyone"); err != nil {
			t.Errorf("checkUser on nil = %v, want nil", err)
		}
		if err := c.checkDatabase("anything"); err != nil {
			t.Errorf("checkDatabase on nil = %v, want nil", err)
		}
	})
}
