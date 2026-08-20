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
	"github.com/golang/glog"
)

var (
	connStr string
	pc      ProxyConfig
)

// pgproxy Main
func Main(config interface{}, pargs interface{}) {
	var proxyconf = flag.String("config", "pgproxy.conf", "configuration file for pgproxy")

	flag.Parse()
	defer glog.Flush()

	if nil != config {
		pc, connStr = readConfig(config.(string))
	} else {
		pc, connStr = readConfig(*proxyconf)
	}

	glog.Infoln("Starting pgproxy...")
	info(pc.ServerConfig.ProxyAddr)

	// Set up signal handling for graceful shutdown
	chExit := make(chan os.Signal, 1)
	signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM)

	// Start the proxy with the configured filter handler
	go func() {
		queryFilter := parser.NewQueryFilter(pc.FilterConfig)
		proxy.Start(pc.ServerConfig.ProxyAddr, pc.DB, queryFilter.Handler)
	}()

	// Block until termination signal is received
	<-chExit
	glog.Infoln("pgproxy shutting down gracefully...")
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
