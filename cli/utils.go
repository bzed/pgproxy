// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package cli provides virtual command-line access
// in pgproxy include start,cli and stop action.
package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/bzed/pgproxy/parser"
	"github.com/bzed/pgproxy/proxy"
)

const Logo = `
    ____  ____ _____  _________  _  ____  __
   / __ \/ __ '/ __ \/ ___/ __ \| |/_/ / / /
  / /_/ / /_/ / /_/ / /  / /_/ />  </ /_/ /
 / .___/\__, / .___/_/   \____/_/|_|\__, /
/_/    /____/_/                    /____/
`

const (
	VERSION = "0.1.0"
)

// proxy server config struct
type ProxyConfig struct {
	ServerConfig struct {
		ProxyAddr string

		// TLSCert/TLSKey, if both set, terminate TLS on ProxyAddr
		// (REVIEW.md H3): a client's SSLRequest is accepted and upgraded
		// instead of always being denied. TLSClientCA additionally
		// requires mutual TLS (the client must present a certificate
		// signed by a CA in that file).
		TLSCert     string
		TLSKey      string
		TLSClientCA string

		// MaxConnections caps concurrent sessions; 0 (default) is
		// unlimited (REVIEW.md M4).
		MaxConnections int

		// IdleTimeout closes a session that goes this long between
		// client messages, e.g. "5m" (REVIEW.md M4); empty (default) is
		// no idle timeout. Parsed with time.ParseDuration.
		IdleTimeout string

		// MetricsLogInterval, if set (e.g. "1m"), logs a snapshot of
		// proxy.Metrics at INFO level on that interval (REVIEW.md M8) -
		// grep/journalctl-friendly monitoring without an added metrics
		// dependency. Empty (default) disables periodic logging; the
		// counters are still collected either way and available to a
		// caller using pgproxy as a library via proxy.WithMetrics.
		MetricsLogInterval string
	}
	DB           map[string]proxy.DBConfig `toml:"DB"`
	FilterConfig parser.FilterConfig       `toml:"Filter"`

	// ACL restricts which clients may connect at all (REVIEW.md H4): see
	// proxy.ACL's doc comment for the exact matching rules. The zero
	// value (no [ACL] section, or an empty one) allows everything, same
	// as omitting it entirely.
	ACL proxy.ACL `toml:"ACL"`
}

// buildStartOptions turns the config sections above into proxy.Option
// values for proxy.Start (REVIEW.md H3/H4/M4). It's a pure function of
// ProxyConfig, kept separate from run() so it's independently testable
// without starting a real proxy.
func buildStartOptions(pc ProxyConfig) ([]proxy.Option, error) {
	var opts []proxy.Option

	if pc.ServerConfig.TLSCert != "" || pc.ServerConfig.TLSKey != "" {
		tlsConfig, err := proxy.NewFrontendTLSConfig(
			pc.ServerConfig.TLSCert, pc.ServerConfig.TLSKey, pc.ServerConfig.TLSClientCA)
		if err != nil {
			return nil, fmt.Errorf("configuring frontend TLS: %w", err)
		}
		opts = append(opts, proxy.WithTLS(tlsConfig))
	}

	if len(pc.ACL.AllowedCIDRs) > 0 || len(pc.ACL.AllowedUsers) > 0 || len(pc.ACL.AllowedDatabases) > 0 {
		opts = append(opts, proxy.WithACL(pc.ACL))
	}

	if pc.ServerConfig.MaxConnections > 0 {
		opts = append(opts, proxy.WithMaxConnections(pc.ServerConfig.MaxConnections))
	}

	if pc.ServerConfig.IdleTimeout != "" {
		d, err := time.ParseDuration(pc.ServerConfig.IdleTimeout)
		if err != nil {
			return nil, fmt.Errorf("parsing ServerConfig.IdleTimeout %q: %w", pc.ServerConfig.IdleTimeout, err)
		}
		opts = append(opts, proxy.WithIdleTimeout(d))
	}

	return opts, nil
}

func readConfig(file string) (ProxyConfig, error) {
	var pc ProxyConfig
	pc.FilterConfig = parser.DefaultFilterConfig()

	if _, err := os.Stat(file); os.IsNotExist(err) {
		return pc, fmt.Errorf("configuration file not found: %w", err)
	}

	if _, err := toml.DecodeFile(file, &pc); err != nil {
		return pc, fmt.Errorf("failed to parse configuration file: %w", err)
	}

	if len(pc.DB) == 0 {
		return pc, fmt.Errorf("configuration error: no databases configured under [DB.*] in configuration file")
	}

	return pc, nil
}

// reloadFilterConfig re-reads file and applies its [Filter] section to
// queryFilter via UpdateConfig (REVIEW.md M8), leaving queryFilter
// unchanged if the reload fails. Split out of run's SIGHUP case so it's
// unit-testable without a live proxy or real signal.
func reloadFilterConfig(file string, queryFilter *parser.QueryFilter) error {
	pc, err := readConfig(file)
	if err != nil {
		return err
	}
	queryFilter.UpdateConfig(pc.FilterConfig)
	return nil
}
