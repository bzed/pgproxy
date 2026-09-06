// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package cli provides virtual command-line access
// in pgproxy include start,cli and stop action.
package cli

import (
	"fmt"
	"os"

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
	}
	DB           map[string]proxy.DBConfig `toml:"DB"`
	FilterConfig parser.FilterConfig       `toml:"Filter"`
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
