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
		"testdb": {
			Addr:     "localhost:5432",
			User:     "postgres",
			Password: "mysecretpassword",
			DBName:   "postgres",
		},
	}
	proxy.Start(
		"localhost:5433",
		dbs,
		func(query string) ([]byte, error) {
			return nil, nil
		},
	)

	// 捕获ctrl-c,平滑退出
	chExit := make(chan os.Signal, 1)
	signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	select {
	case <-chExit:
		fmt.Println("Example EXITING...Bye.")
	}
}
