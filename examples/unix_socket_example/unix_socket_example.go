package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bzed/pgproxy/proxy"
)

func main() {
	dbs := map[string]proxy.DBConfig{
		"postgres": {
			Addr:     "/var/run/postgresql/.s.PGSQL.5432", // Backend Unix Socket
			User:     "postgres",
			Password: "mysecretpassword",
			DBName:   "postgres",
		},
	}
	
	// Listen on a Unix socket instead of TCP
	proxy.Start(
		"/tmp/.s.PGSQL.5433",
		dbs,
		func(query string) ([]byte, error) {
			return nil, nil // Passthrough
		},
	)

	// Catch ctrl-c to exit gracefully
	chExit := make(chan os.Signal, 1)
	signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	<-chExit
	fmt.Println("Example EXITING...Bye.")
}
