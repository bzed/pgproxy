![pgproxy](./pgproxy.png)

# pgproxy
[![Build Status](https://github.com/bzed/pgproxy/actions/workflows/go-test.yml/badge.svg?branch=master)](https://github.com/bzed/pgproxy/actions/workflows/go-test.yml)
[![codecov](https://codecov.io/gh/bzed/pgproxy/branch/master/graph/badge.svg)](https://codecov.io/gh/bzed/pgproxy)
[![GoDoc](https://pkg.go.dev/badge/github.com/bzed/pgproxy.svg)](https://pkg.go.dev/github.com/bzed/pgproxy)
[![License](https://img.shields.io/badge/LICENSE-Apache2.0-ff69b4.svg)](http://www.apache.org/licenses/LICENSE-2.0.html)

pgproxy is a PostgreSQL proxy server that uses pipe redirect connections to filter requested SQL statements. In the future it will support multi-database backup, distributed database adaptation, and other features beyond SQL analysis.

## Features

* Database read/write separation
* Database services disaster recovery
* Proxy database connections
* Rewrite SQL statements
* Filter dangerous SQL
* Monitor database operations
* SQL request current limiting and merging

## Installation

```
$ go get -u github.com/bzed/pgproxy
```

## Using

### As a separate application

Start or shut down the proxy server.
```
$ pgproxy start/stop
```

Use pgproxy on the command line
```
$ pgproxy cli
```

Note: You can use it as you would with a native command line.

### Be called as a package

[package_example](https://github.com/bzed/pgproxy/blob/master/examples/package_example.go)

```
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bzed/pgproxy/cli"
)

func main() {
	// call proxy
	cli.Main("../pgproxy.conf", []string{"pgproxy", "start"})

	// Capture ctrl-c for graceful exit
	chExit := make(chan os.Signal, 1)
	signal.Notify(chExit, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	select {
	case <-chExit:
		fmt.Println("Example EXITING...Bye.")
	}
}

```

## SQL Support

Supports SELECT, DELETE, UPDATE statements in any case.

### SQL Standard Support:

The parser is forked from vitess's [sqlparser](https://github.com/youtube/vitess/tree/master/go/vt/sqlparser) of YouTube.

In pgproxy, database tables are like MySQL(5.6,5.7) relational tables, and you can use relational modeling schemes (normalization) to structure your schema. It supports almost all MySQL(5.6,5.7) scalar data types. It also provides full SQL support within a shard, including JOIN statements. Some PostgreSQL operations are not supported; detail see [supported types and keywords](https://github.com/bzed/pgproxy/blob/master/parser/token.go#L37).


## Credits

Package parser is based on [sqlparser](https://github.com/xwb1989/sqlparser)
