package proxy

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgproto3/v2"
)

// DBConfig holds the configuration for a target database
type DBConfig struct {
	Addr   string
	DBName string
}

// readStartupMessage reads the initial packets from the client.
// It handles SSLRequest by denying it ('N') and then reads the actual StartupMessage.
func readStartupMessage(conn net.Conn) (map[string]string, []byte, error) {
	for {
		var lenBuf [4]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return nil, nil, err
		}
		length := binary.BigEndian.Uint32(lenBuf[:])
		if length < 8 || length > 10000 {
			return nil, nil, fmt.Errorf("invalid startup message length: %d", length)
		}

		payload := make([]byte, length-4)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return nil, nil, err
		}

		code := binary.BigEndian.Uint32(payload[:4])
		if code == 80877103 { // SSLRequest
			_, _ = conn.Write([]byte{'N'}) // Deny SSL for now
			continue
		}
		if code == 80877102 { // CancelRequest
			// Not handling correctly in multi-db yet, but just pass through for now
			return nil, append(lenBuf[:], payload...), nil
		}
		if code == 196608 { // StartupMessage
			params := parseStartupParams(payload[4:])
			return params, append(lenBuf[:], payload...), nil
		}
		return nil, nil, fmt.Errorf("unknown startup code: %d", code)
	}
}

func parseStartupParams(data []byte) map[string]string {
	params := make(map[string]string)
	buf := data
	for len(buf) > 0 {
		idx := bytes.IndexByte(buf, 0)
		if idx <= 0 {
			break
		}
		key := string(buf[:idx])
		buf = buf[idx+1:]

		idx = bytes.IndexByte(buf, 0)
		if idx < 0 {
			break
		}
		val := string(buf[:idx])
		buf = buf[idx+1:]

		params[key] = val
	}
	return params
}

// buildStartupMessage constructs a StartupMessage with overridden user and database.

// connectBackend connects to the backend database, handles SSL and Authentication,
// and leaves the connection in a state ready to be piped to the client.
func connectBackend(db DBConfig, startupMsg []byte) (net.Conn, error) {
	network := "tcp"
	addr := db.Addr
	if strings.HasPrefix(addr, "/") {
		network = "unix"
	} else if strings.HasPrefix(addr, "unix:") {
		network = "unix"
		addr = strings.TrimPrefix(addr, "unix:")
	}

	conn, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}

	// 1. Send SSLRequest
	sslReq := (&pgproto3.SSLRequest{}).Encode(nil)
	if _, err := conn.Write(sslReq); err != nil {
		conn.Close()
		return nil, err
	}
	var sslResp [1]byte
	if _, err := io.ReadFull(conn, sslResp[:]); err != nil {
		conn.Close()
		return nil, err
	}
	if sslResp[0] == 'S' {
		conn = tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	}

	// 2. Pass through the client's original StartupMessage
	if _, err := conn.Write(startupMsg); err != nil {
		conn.Close()
		return nil, err
	}

	// We are done! Return the connection to the proxy handler so it can seamlessly proxy the Auth requests/responses.
	return conn, nil
}

// peekedConn wraps a net.Conn and replays peeked bytes first
