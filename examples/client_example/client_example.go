package main

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func main() {
	// dbname must match a [DB.*] key in pgproxy.conf (pgproxy routes on the
	// StartupMessage's "database" parameter, not a real backend database
	// name) - "master" here matches the shipped pgproxy.conf's [DB.master].
	db, err := sqlx.Open("postgres", "host=127.0.0.1 port=9090 user=postgres password=postgres dbname=master sslmode=disable")
	if err != nil {
		fmt.Println(err)
	}

	// Set connections num
	db.SetMaxIdleConns(5)
	db.SetMaxOpenConns(100)

	defer func() {
		db.Close()
		if err != nil {
			fmt.Println(err)
		}
	}()

	rows, err := db.Query("select id from client where id < 10")
	if err != nil {
		fmt.Println(err)
	} else {
		for rows.Next() {
			var id int64

			if err := rows.Scan(&id); err != nil {
				fmt.Println(err)
			} else {
				fmt.Println(id)
			}
		}
	}
}
