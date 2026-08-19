![pgproxy](./pgproxy.png)

# pgproxy
[![Build Status](https://github.com/bzed/pgproxy/actions/workflows/go-test.yml/badge.svg?branch=master)](https://github.com/bzed/pgproxy/actions/workflows/go-test.yml)
[![codecov](https://codecov.io/gh/bzed/pgproxy/branch/master/graph/badge.svg)](https://codecov.io/gh/bzed/pgproxy)
[![GoDoc](https://pkg.go.dev/badge/github.com/bzed/pgproxy.svg)](https://pkg.go.dev/github.com/bzed/pgproxy)
[![License](https://img.shields.io/badge/LICENSE-Apache2.0-ff69b4.svg)](http://www.apache.org/licenses/LICENSE-2.0.html)

pgproxy is a PostgreSQL proxy server that uses pipe redirect connections to filter requested SQL statements. In the future it will support multi-database backup, distributed database adaptation, and other features beyond SQL analysis.

## Features

* Proxy database connections
* Configurable SQL Firewall (block un-safe mutations and destructive queries)
* Signature-based query filtering (pgBouncer style fingerprinting)
* Native PostgreSQL parsing engine (based on CockroachDB parser)

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

### Using Configuration

pgproxy is configured using a TOML file (default: `pgproxy.conf`).

```toml
[ServerConfig]
    ProxyAddr = "127.0.0.1:9090"

[DB]
    [DB.master]
        Addr = "127.0.0.1:5432"
        User = "postgres"
        Password = "testpass"
        DbName = "testdb"

[Filter]
    allow_select = true
    allow_insert = true
    allow_update = true
    allow_delete = true
    
    allow_truncate = false
    allow_alter_role = false
    
    require_where_for_update = true
    require_where_for_delete = true

    # block_signatures = ["SELECT * FROM users WHERE (id = _) AND (name = _)"]
    # allow_signatures = ["SELECT id FROM allowed_table"]
```

### Be called as a package

[package_example](https://github.com/bzed/pgproxy/blob/master/examples/package_example/package_example.go)

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

pgproxy uses [postgresql-parser](https://github.com/auxten/postgresql-parser), a robust SQL parser extracted from CockroachDB. This provides comprehensive, native support for PostgreSQL syntax, data types, and keywords, making it far superior to legacy MySQL-based parsers.

Supports parsing and rewriting for a broad array of PostgreSQL statements including `SELECT`, `INSERT`, `UPDATE`, `DELETE`, and many advanced SQL operations.

## Credits

Package parser utilizes [postgresql-parser](https://github.com/auxten/postgresql-parser) (derived from CockroachDB's parsing engine).
