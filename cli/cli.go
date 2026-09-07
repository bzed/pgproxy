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
// shuts the proxy down gracefully - a SIGHUP instead reloads the [Filter]
// rules from configPath without dropping connections (REVIEW.md M8; see
// run's doc comment for what SIGHUP does and does not reload). It is a thin
// wrapper around mainBody() that wires up the real process-wide flag set
// and OS signal channels; mainBody() (and run(), which it calls) carry the
// actual logic and are what tests exercise directly, since calling Main
// more than once per test binary would panic on flag redefinition.
func Main(configPath string) {
	proxyconf := flag.String("config", "pgproxy.conf", "configuration file for pgproxy")
	showVersion := flag.Bool("version", false, "print the pgproxy version and exit")
	flag.Parse()
	defer glog.Flush()

	chExit := make(chan os.Signal, 1)
	signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM)
	chReload := make(chan os.Signal, 1)
	signal.Notify(chReload, syscall.SIGHUP)
	mainBody(configPath, *proxyconf, *showVersion, chExit, chReload)
}

// mainBody is Main's actual logic, taking the parsed flag values as plain
// arguments so it - unlike Main itself - can be unit-tested repeatedly
// without touching global flag.CommandLine state.
func mainBody(configPath, proxyconf string, showVersion bool, chExit, chReload <-chan os.Signal) {
	if showVersion {
		fmt.Println(VERSION)
		return
	}

	if configPath == "" {
		configPath = proxyconf
	}
	run(configPath, chExit, chReload)
}

// run reads the configuration, starts the proxy, and blocks until chExit
// receives a value, then shuts the proxy down gracefully. A value on
// chReload instead re-reads configPath and hot-swaps the [Filter] rules via
// QueryFilter.UpdateConfig, without dropping the listener or any live
// session (REVIEW.md M8). Only [Filter] is reloadable this way: ServerConfig
// (address, TLS, ACL, connection limits) and [DB.*] require a full restart,
// since changing them would mean rebinding the listener or renegotiating
// already-open sessions.
//
// Split out of Main so it can be driven with synthetic channels in tests,
// without touching process-wide flag registration or real OS signals.
func run(configPath string, chExit, chReload <-chan os.Signal) {
	pc, err := readConfig(configPath)
	if err != nil {
		glog.Fatalln(err)
	}

	glog.Infoln("Starting pgproxy...")
	info(pc.ServerConfig.ProxyAddr)

	opts, err := buildStartOptions(pc)
	if err != nil {
		glog.Fatalf("Invalid configuration: %v", err)
	}

	// Metrics are always collected (REVIEW.md M8) - MetricsLogInterval
	// only controls whether they're also logged periodically; a caller
	// using pgproxy as a library can read metrics directly at any time.
	metrics := &proxy.Metrics{}
	opts = append(opts, proxy.WithMetrics(metrics))

	queryFilter := parser.NewQueryFilter(pc.FilterConfig)
	stop, err := proxy.Start(pc.ServerConfig.ProxyAddr, pc.DB, queryFilter.Handler, opts...)
	if err != nil {
		glog.Fatalf("Failed to start proxy: %v", err)
	}

	stopMetricsLog := func() {}
	if pc.ServerConfig.MetricsLogInterval != "" {
		interval, err := time.ParseDuration(pc.ServerConfig.MetricsLogInterval)
		if err != nil {
			glog.Fatalf("Invalid configuration: parsing ServerConfig.MetricsLogInterval %q: %v",
				pc.ServerConfig.MetricsLogInterval, err)
		}
		stopMetricsLog = logMetricsPeriodically(metrics, interval)
	}

	// Block until a termination signal is received, reloading the filter
	// config on every SIGHUP in the meantime.
waitLoop:
	for {
		select {
		case <-chExit:
			break waitLoop
		case <-chReload:
			if err := reloadFilterConfig(configPath, queryFilter); err != nil {
				glog.Errorf("SIGHUP: failed to reload %s, keeping the current filter config: %v", configPath, err)
				continue
			}
			glog.Infof("SIGHUP: reloaded filter configuration from %s", configPath)
		}
	}

	stopMetricsLog()
	glog.Infoln("pgproxy shutting down gracefully...")
	if ok, err := daemon.SdNotify(false, daemon.SdNotifyStopping); err != nil {
		glog.Errorf("Failed to notify systemd of stopping: %v", err)
	} else if ok {
		glog.Infoln("Systemd notified of stopping")
	}
	stop()
}

// logMetricsPeriodically logs an INFO-level snapshot of metrics every
// interval (REVIEW.md M8: grep/journalctl-friendly monitoring without an
// added metrics dependency), until the returned stop function is called.
func logMetricsPeriodically(metrics *proxy.Metrics, interval time.Duration) (stop func()) {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s := metrics.Snapshot()
				glog.Infof("METRICS: total_connections=%d active_connections=%d "+
					"rejected_by_acl=%d rejected_by_max_connections=%d blocked_queries=%d backend_connect_errors=%d",
					s.TotalConnections, s.ActiveConnections, s.RejectedByACL,
					s.RejectedByMaxConnections, s.BlockedQueries, s.BackendConnectErrors)
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
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
