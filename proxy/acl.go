package proxy

import (
	"fmt"
	"net"
)

// ACL controls which clients may open a session and which database they may
// reach (REVIEW.md H4). Every populated list is an allowlist: a session
// must match every non-empty list to proceed. An empty (nil or
// zero-length) list imposes no restriction for that dimension - so the
// zero value ACL{} allows everything, same as not passing WithACL at all.
//
// Checks run in this order, each failing closed with a FATAL ErrorResponse
// and no session started:
//  1. AllowedCIDRs, against the client's source IP, at accept time (before
//     the client has sent anything).
//  2. AllowedDatabases, against the StartupMessage's "database" parameter
//     (the dbs map key the client requested, not the backend's real
//     database name) - this also implicitly restricts routing to a subset
//     of the dbs map Start was given.
//  3. AllowedUsers, against the StartupMessage's "user" parameter.
type ACL struct {
	AllowedCIDRs     []string `toml:"allowed_cidrs"`
	AllowedUsers     []string `toml:"allowed_users"`
	AllowedDatabases []string `toml:"allowed_databases"`
}

// compiledACL is ACL with AllowedCIDRs pre-parsed once (at Start time)
// rather than on every connection.
type compiledACL struct {
	cidrs            []*net.IPNet
	allowedUsers     map[string]bool
	allowedDatabases map[string]bool
}

func compileACL(acl *ACL) (*compiledACL, error) {
	if acl == nil {
		return nil, nil
	}
	c := &compiledACL{}
	for _, s := range acl.AllowedCIDRs {
		_, ipnet, err := net.ParseCIDR(s)
		if err != nil {
			// A bare IP (no /mask) is a common typo for a /32 (or /128);
			// accept it rather than failing configuration for something
			// libpq's own pg_hba.conf accepts too.
			if ip := net.ParseIP(s); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				ipnet = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
			} else {
				return nil, fmt.Errorf("invalid ACL AllowedCIDRs entry %q: %w", s, err)
			}
		}
		c.cidrs = append(c.cidrs, ipnet)
	}
	if len(acl.AllowedUsers) > 0 {
		c.allowedUsers = make(map[string]bool, len(acl.AllowedUsers))
		for _, u := range acl.AllowedUsers {
			c.allowedUsers[u] = true
		}
	}
	if len(acl.AllowedDatabases) > 0 {
		c.allowedDatabases = make(map[string]bool, len(acl.AllowedDatabases))
		for _, d := range acl.AllowedDatabases {
			c.allowedDatabases[d] = true
		}
	}
	return c, nil
}

// checkAddr reports whether addr (a client's remote address, e.g. from
// net.Conn.RemoteAddr()) is allowed by AllowedCIDRs. Always true when no
// CIDRs are configured.
func (c *compiledACL) checkAddr(addr net.Addr) error {
	if c == nil || len(c.cidrs) == 0 {
		return nil
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String() // e.g. a unix socket address has no port
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A unix socket peer has no IP at all; treat as un-restrictable by
		// CIDR rather than rejecting it outright, since CIDR ACLs are a
		// TCP-only concept.
		return nil
	}
	for _, ipnet := range c.cidrs {
		if ipnet.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("client address %s not in an allowed CIDR", host)
}

// checkUser reports whether user is allowed by AllowedUsers. Always true
// when no users are configured.
func (c *compiledACL) checkUser(user string) error {
	if c == nil || c.allowedUsers == nil {
		return nil
	}
	if !c.allowedUsers[user] {
		return fmt.Errorf("user %q is not allowed", user)
	}
	return nil
}

// checkDatabase reports whether database (the dbs map key the client
// requested) is allowed by AllowedDatabases. Always true when no databases
// are configured.
func (c *compiledACL) checkDatabase(database string) error {
	if c == nil || c.allowedDatabases == nil {
		return nil
	}
	if !c.allowedDatabases[database] {
		return fmt.Errorf("database %q is not allowed", database)
	}
	return nil
}
