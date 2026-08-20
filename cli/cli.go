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

	var args []string
	if nil != config {
		pc, connStr = readConfig(config.(string))
		args = pargs.([]string)
	} else {
		pc, connStr = readConfig(*proxyconf)
		args = os.Args
		fmt.Print(args)
	}

	if len(args) < 2 {
		glog.Errorln("needed one parameters:", args)
		help()
		return
	} else {
		if args[1] == "start" {
			glog.Infoln("Starting pgproxy...")
			info(pc.ServerConfig.ProxyAddr)
			logDir()
			saveCurrentPid()

			// Set up signal handling for graceful shutdown
			chExit := make(chan os.Signal, 1)
			signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM)

			// Start the proxy with the configured filter handler
			go func() {
				queryFilter := parser.NewQueryFilter(pc.FilterConfig)
				proxy.Start(pc.ServerConfig.ProxyAddr, pc.DB, queryFilter.Handler)
			}()
			glog.Infoln("Started pgproxy successfully.")

			// Block until termination signal is received
			<-chExit
			glog.Infoln("pgproxy shutting down gracefully...")
			os.Remove("./log/pid.log")
		} else if args[1] == "cli" {
			Command()
		} else if args[1] == "stop" {
			stop()
		} else {
			help()
		}
	}
}

// print pgproxy help
func help() {
	fmt.Println("	pgproxy is a proxy-server for database postgresql.")
	fmt.Println("	start :start pgproxy server.")
	fmt.Println("	stop :stop pgproxy server.")
	fmt.Println("	version :pgproxy version.")
	fmt.Println("	info :pgproxy info.")
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

// set log dir
func logDir() {
	_, err := os.Stat("./log")
	if err != nil && os.IsNotExist(err) {
		err := os.MkdirAll("./log", 0777)
		if err != nil {
			glog.Fatalln(err)
		} else {
			glog.Infoln("glog and process pid in ./log")
		}
	}
}

// save current pgproxy pid
func saveCurrentPid() {
	// pid file
	filepath := "./log/pid.log"
	fout, err := os.OpenFile(filepath, os.O_CREATE|os.O_RDWR, 0777)
	if err != nil {
		glog.Errorln(err)
		return
	}
	defer fout.Close()
	// write current pid
	_, _ = fout.WriteString(strconv.Itoa(os.Getpid()))
}

// get current pgproxy pid
func getCurrentPid() int {
	// pid file
	filepath := "./log/pid.log"
	fin, err := os.OpenFile(filepath, os.O_RDONLY, 0777)
	if err != nil {
		glog.Errorln(err)
		return 0
	}
	defer fin.Close()
	// read current pid
	buf := make([]byte, 1024)

	n, _ := fin.Read(buf)
	if 0 >= n {
		return 0
	} else {
		pid, err := strconv.Atoi(string(buf[0:n]))
		if err != nil {
			glog.Errorln(err)
			return 0
		} else {
			return pid
		}
	}
}

// stop pgproxy
func stop() {
	pid := getCurrentPid()
	if pid != 0 {
		// Use os.Process.Signal for graceful cross-platform compatibility
		process, err := os.FindProcess(pid)
		if err != nil {
			glog.Errorln(err)
		} else {
			err = process.Signal(syscall.SIGTERM)
			if err != nil {
				glog.Errorln("Failed to send SIGTERM:", err)
			} else {
				glog.Infoln("pgproxy stop signal sent successfully!")
			}
		}
	}
	fmt.Printf("pgproxy(%d) Exit,thanks.\n", pid)
}
