// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package cli provides virtual command-line access
// in pgproxy include start,cli and stop action.
package cli

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/bzed/pgproxy/parser"
	"github.com/bzed/pgproxy/proxy"
	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/golang/glog"
)

// Main starts pgproxy using the TOML configuration at configPath. An empty
// configPath falls back to the -config flag (default "pgproxy.conf"), unless
// -version was given, in which case Main prints the version and returns
// immediately. Otherwise Main blocks until it receives SIGINT/SIGTERM, then
// shuts the proxy down gracefully. It is a thin wrapper around mainBody()
// that wires up the real process-wide flag set and OS signal channel;
// mainBody() (and run(), which it calls) carry the actual logic and are what
// tests exercise directly, since calling Main more than once per test binary
// would panic on flag redefinition.
func Main(configPath string) {
	proxyconf := flag.String("config", "pgproxy.conf", "configuration file for pgproxy")
	showVersion := flag.Bool("version", false, "print the pgproxy version and exit")
	flag.Parse()
	defer glog.Flush()

	chExit := make(chan os.Signal, 1)
	signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM)
	mainBody(configPath, *proxyconf, *showVersion, chExit)
}

// mainBody is Main's actual logic, taking the parsed flag values as plain
// arguments so it - unlike Main itself - can be unit-tested repeatedly
// without touching global flag.CommandLine state.
func mainBody(configPath, proxyconf string, showVersion bool, chExit <-chan os.Signal) {
	if showVersion {
		fmt.Println(VERSION)
		return
	}

	if configPath == "" {
		configPath = proxyconf
	}
	run(configPath, chExit)
}

// run reads the configuration, starts the proxy, and blocks until chExit
// receives a value, then shuts the proxy down gracefully. Split out of Main
// so it can be driven with a synthetic channel in tests, without touching
// process-wide flag registration or real OS signals.
func run(configPath string, chExit <-chan os.Signal) {
	pc, err := readConfig(configPath)
	if err != nil {
		glog.Fatalln(err)
	}

	glog.Infoln("Starting pgproxy...")
	info(pc.ServerConfig.ProxyAddr)

	queryFilter := parser.NewQueryFilter(pc.FilterConfig)
	stop, err := proxy.Start(pc.ServerConfig.ProxyAddr, pc.DB, queryFilter.Handler)
	if err != nil {
		glog.Fatalf("Failed to start proxy: %v", err)
	}

	// Block until termination signal is received, then shut down gracefully.
	<-chExit

	glog.Infoln("pgproxy shutting down gracefully...")
	if ok, err := daemon.SdNotify(false, daemon.SdNotifyStopping); err != nil {
		glog.Errorf("Failed to notify systemd of stopping: %v", err)
	} else if ok {
		glog.Infoln("Systemd notified of stopping")
	}
	stop()
}

// print pgproxy information
func info(proxyhost string) {
	fmt.Print(Logo)
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "<unknown>"
	}
	pid := strconv.Itoa(os.Getpid())
	starttime := time.Now().Format("2006-01-02 03:04:05 PM")
	fmt.Printf("  %s\n", VERSION)
	fmt.Printf("  Host: %s\n", hostname)
	fmt.Printf("  Pid: %s\n", pid)
	fmt.Printf("  Proxy: %s\n", proxyhost)
	fmt.Printf("  Starttime: %s\n", starttime)
}
