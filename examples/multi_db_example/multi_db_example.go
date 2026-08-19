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
		"primary": {
			Addr:     "localhost:5432",
			User:     "postgres",
			Password: "mysecretpassword",
			DBName:   "primary_db",
		},
		"analytics": {
			Addr:     "10.0.0.5:5432",
			User:     "analytics_user",
			Password: "analyticspassword",
			DBName:   "analytics_db",
		},
	}
	
	// Start proxy on port 5433, connections to database "primary" 
	// will route to localhost:5432, while "analytics" routes to 10.0.0.5
	proxy.Start(
		"localhost:5433",
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
