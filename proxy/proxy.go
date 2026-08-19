// Copyright 2017 wgliang. All rights reserved.
// Use of this source code is governed by Apache
// license that can be found in the LICENSE file.

// Package proxy provides proxy service and redirects requests
// form proxy.Addr to remote.Addr.
package proxy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	
	"github.com/jackc/pgproto3/v2"
	"github.com/golang/glog"
)

var (
	connid = uint64(0) // Self-increasing ConnectID.
)

// Handler function from proxy to postgresql for rewrite
// request or sql. Receives the query string and returns modified bytes.
type Handler func(query string) ([]byte, error)

// Start proxy server needed receive proxyHost, and database configs
func Start(proxyHost string, dbs map[string]DBConfig, handler Handler) {
	defer glog.Flush()
	glog.Infof("Proxying from %v with %d configured databases\n", proxyHost, len(dbs))

	listener := getListener(proxyHost)

	for {
		conn, err := listener.Accept()
		if err != nil {
			glog.Errorf("Failed to accept connection '%s'\n", err)
			continue
		}
		connid++

		p := &Proxy{
			lconn:   conn,
			erred:   false,
			errsig:  make(chan bool),
			prefix:  fmt.Sprintf("Connection #%03d ", connid),
			connID:  connid,
			bufPool: &bufferPool{},
		}
		go p.service(dbs, handler)
	}
}

// Listener of a net.Addr.
func getListener(host string) net.Listener {
	var listener net.Listener
	var err error
	if strings.HasPrefix(host, "/") || strings.HasPrefix(host, "unix:") {
		host = strings.TrimPrefix(host, "unix:")
		listener, err = net.Listen("unix", host)
	} else {
		listener, err = net.Listen("tcp", host)
	}
	if err != nil {
		glog.Fatalf("Listen on %s error:%v", host, err)
	}
	return listener
}

// Proxy - Manages a Proxy connection, piping data between proxy and remote.
type Proxy struct {
	lconn, rconn net.Conn
	erred        bool
	errsig       chan bool
	prefix       string
	connID       uint64
	bufPool      *bufferPool
}

// bufferPool provides buffer pooling for performance optimization
type bufferPool struct {
	pool sync.Pool
}

func (bp *bufferPool) Get() []byte {
	if b := bp.pool.Get(); b != nil {
		return *(b.(*[]byte))
	}
	return make([]byte, 65536) // 64KB default buffer
}

func (bp *bufferPool) Put(b []byte) {
	// Only pool buffers that are reasonably sized
	if cap(b) >= 4096 && cap(b) <= 65536 {
		b = b[:cap(b)]
		bp.pool.Put(&b)
	}
}

// New - Create a new Proxy instance. Takes over local connection passed in,
// and closes it when finished.
func New(conn net.Conn, connid uint64) *Proxy {
	return &Proxy{
		lconn:   conn,
		erred:   false,
		errsig:  make(chan bool),
		prefix:  fmt.Sprintf("Connection #%03d ", connid),
		connID:  connid,
		bufPool: &bufferPool{},
	}
}

// proxy.err
func (p *Proxy) err(s string, err error) {
	if p.erred {
		return
	}
	if err != io.EOF {
		glog.Errorf(p.prefix+s, err)
	}
	p.errsig <- true
	p.erred = true
}

// Proxy.service open connection to remote and service proxying data.
func (p *Proxy) service(dbs map[string]DBConfig, handler Handler) {
	defer p.lconn.Close()

	// 1. Read StartupMessage from client
	params, _, err := readStartupMessage(p.lconn)
	if err != nil {
		p.err("Failed to read startup message: %s", err)
		return
	}
	if params == nil {
		p.err("Unsupported cancel request or empty startup", nil)
		return
	}

	dbName := params["database"]
	dbConf, ok := dbs[dbName]
	if !ok {
		errResp := buildErrorResponse("FATAL", "database not found in proxy config: "+dbName)
		_, _ = p.lconn.Write(errResp)
		p.err("Database not configured: "+dbName, nil)
		return
	}

	// 2. Connect to backend and handle auth
	rconn, err := connectBackend(dbConf)
	if err != nil {
		errResp := buildErrorResponse("FATAL", "backend connection failed: "+err.Error())
		_, _ = p.lconn.Write(errResp)
		p.err("Remote connection failed: %s", err)
		return
	}
	p.rconn = rconn
	defer p.rconn.Close()

	// proxying data
	go p.handleIncomingConnection(p.lconn, p.rconn, handler)
	go p.handleResponseConnection(p.rconn, p.lconn)

	// wait for close...
	<-p.errsig
}

func buildErrorResponse(severity, message string) []byte {
	errResp := &pgproto3.ErrorResponse{
		Severity: severity,
		Message:  message,
	}
	return errResp.Encode(nil)
}

// PostgreSQL message types
const (
	SimpleQuery = 'Q' // Simple Query message
	ParseMsg    = 'P' // Parse message
	BindMsg     = 'B' // Bind message
	ExecuteMsg  = 'E' // Execute message
	DescribeMsg = 'D' // Describe message
	CloseMsg    = 'C' // Close message
	SyncMsg     = 'S' // Sync message
	Terminate   = 'X' // Terminate message
)

// isQueryMessage returns true for message types that might contain SQL
func isQueryMessage(msgType byte) bool {
	switch msgType {
	case SimpleQuery, ParseMsg, BindMsg:
		return true
	}
	return false
}

// Proxy.handleIncomingConnection processes incoming client messages
func (p *Proxy) handleIncomingConnection(src, dst net.Conn, customHandler Handler) {
	buff := p.bufPool.Get()
	defer p.bufPool.Put(buff)

	for {
		// Read the first byte to determine message format
		_, err := io.ReadFull(src, buff[:1])
		if err != nil {
			if err == io.EOF {
				p.err("Client closed connection", err)
			} else {
				p.err("Read header failed: %s\n", err)
			}
			return
		}

		var msgType byte
		var msgLength uint32
		var headerSize int

		// If the first byte is 0x00, it's a StartupMessage, SSLRequest, or CancelRequest.
		// These messages do not have a 1-byte message type; they start directly with a 4-byte length.
		if buff[0] == 0 {
			// Read the remaining 3 bytes of the 4-byte length
			_, err = io.ReadFull(src, buff[1:4])
			if err != nil {
				p.err("Read length failed: %s\n", err)
				return
			}
			msgType = 0
			msgLength = binary.BigEndian.Uint32(buff[:4])
			headerSize = 4
		} else {
			// Normal message: 1-byte type followed by 4-byte length
			msgType = buff[0]
			_, err = io.ReadFull(src, buff[1:5])
			if err != nil {
				p.err("Read length failed: %s\n", err)
				return
			}
			msgLength = binary.BigEndian.Uint32(buff[1:5])
			headerSize = 5
		}

		totalContentLength := int(msgLength) - 4
		if totalContentLength < 0 {
			p.err("Invalid message length", fmt.Errorf("invalid message length: %d", msgLength))
			return
		}

		// Ensure buffer is large enough
		if len(buff) < headerSize+totalContentLength {
			newBuff := make([]byte, headerSize+totalContentLength)
			copy(newBuff, buff[:headerSize])
			p.bufPool.Put(buff)
			buff = newBuff
		}

		// Read the rest of the message content
		if totalContentLength > 0 {
			_, err = io.ReadFull(src, buff[headerSize:headerSize+totalContentLength])
			if err != nil {
				p.err("Read content failed: %s\n", err)
				return
			}
		}

		fullMsg := buff[:headerSize+totalContentLength]

		// Process the message if it's a query type (only normal messages have a type)
		if msgType != 0 && isQueryMessage(msgType) && totalContentLength > 0 {
			content := buff[headerSize : headerSize+totalContentLength]
			modifiedContent, err := HandleQuery(msgType, content, customHandler)
			if err != nil {
				p.err("Query handling error: %s\n", err)
				return
			}

			// Reconstruct message if content was modified
			if modifiedContent != nil && !bytes.Equal(modifiedContent, content) {
				newMsg := make([]byte, 5+len(modifiedContent))
				newMsg[0] = msgType
				binary.BigEndian.PutUint32(newMsg[1:5], uint32(len(modifiedContent)+4))
				copy(newMsg[5:], modifiedContent)
				fullMsg = newMsg
			}
		}

		// Write to destination
		_, err = dst.Write(fullMsg)
		if err != nil {
			p.err("Write failed: %s\n", err)
			return
		}
	}
}

// Proxy.handleResponseConnection forwards server responses to client
func (p *Proxy) handleResponseConnection(src, dst net.Conn) {
	// Server -> Client messages do not need to be parsed by this proxy.
	// A simple io.Copy prevents deadlocks with 1-byte responses like SSLRequest's 'S' or 'N'.
	_, err := io.Copy(dst, src)
	if err != nil && err != io.EOF {
		p.err("Server response error: %s\n", err)
	}
}

// HandleQuery processes query content and applies the handler
func HandleQuery(msgType byte, content []byte, requestHandler Handler) ([]byte, error) {
	// Remove null terminator if present
	queryStr := string(bytes.TrimSuffix(content, []byte{0}))

	// Call handler with query string
	data, err := requestHandler(queryStr)
	if err != nil {
		return nil, fmt.Errorf("handler error: %w", err)
	}

	// If handler returns data, use it
	if data != nil {
		// Ensure null terminator
		if len(data) == 0 || data[len(data)-1] != 0 {
			data = append(data, 0)
		}
		return data, nil
	}

	// Return original content with null terminator
	if len(content) == 0 || content[len(content)-1] != 0 {
		return append(content, 0), nil
	}
	return content, nil
}
