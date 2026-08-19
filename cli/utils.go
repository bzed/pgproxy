// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package cli provides virtual command-line access
// in pgproxy include start,cli and stop action.
package cli

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/bzed/pgproxy/parser"
	"github.com/bzed/pgproxy/proxy"
	"github.com/golang/glog"
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

func readConfig(file string) (pc ProxyConfig, connStr string) {
	pc.FilterConfig = parser.DefaultFilterConfig()

	if _, err := os.Stat(file); os.IsNotExist(err) {
		glog.Errorln("Configuration file not found:", err)
		os.Exit(int(syscall.ENOENT))
	}

	if _, err := toml.DecodeFile(file, &pc); err != nil {
		glog.Fatalln("Failed to parse configuration file:", err)
	}

	// Check if master database is configured
	if _, ok := pc.DB["master"]; !ok {
		glog.Fatalln("Configuration error: DB.master not found in configuration file")
	}

	master := pc.DB["master"]
	sepindex := strings.Index(master.Addr, ":")

	if sepindex == -1 {
		glog.Fatalln("Invalid database address format in configuration. Expected 'host:port'")
	}

	return pc, fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s application_name=pgproxy sslmode=disable",
		master.Addr[0:sepindex], master.Addr[(sepindex+1):], master.User, master.Password, master.DBName)
}
