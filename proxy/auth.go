package proxy

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/lib/pq/scram"
)

// DBConfig holds the configuration for a target database
type DBConfig struct {
	Addr     string
	User     string
	Password string
	DBName   string
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
func buildStartupMessage(params map[string]string, user, dbname string) []byte {
	// copy params
	newParams := make(map[string]string)
	for k, v := range params {
		newParams[k] = v
	}
	newParams["user"] = user
	newParams["database"] = dbname

	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0}) // placeholder for length
	buf.Write([]byte{0, 3, 0, 0}) // version 3.0

	for k, v := range newParams {
		buf.WriteString(k)
		buf.WriteByte(0)
		buf.WriteString(v)
		buf.WriteByte(0)
	}
	buf.WriteByte(0)

	out := buf.Bytes()
	binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
	return out
}

// connectBackend connects to the backend database, handles SSL and Authentication,
// and leaves the connection in a state ready to be piped to the client.
func connectBackend(db DBConfig) (net.Conn, error) {
	conn, err := net.Dial("tcp", db.Addr)
	if err != nil {
		return nil, err
	}

	// 1. Send SSLRequest
	sslReq := []byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}
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

	// 2. Send StartupMessage
	params := map[string]string{
		"user":             db.User,
		"database":         db.DBName,
		"application_name": "pgproxy",
	}
	startupMsg := buildStartupMessage(params, db.User, db.DBName)
	if _, err := conn.Write(startupMsg); err != nil {
		conn.Close()
		return nil, err
	}

	// 3. Handle Authentication
	var saslClient *scram.Client
	for {
		var header [5]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			conn.Close()
			return nil, err
		}
		msgType := header[0]
		msgLen := binary.BigEndian.Uint32(header[1:5]) - 4

		payload := make([]byte, msgLen)
		if msgLen > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				conn.Close()
				return nil, err
			}
		}

		if msgType == 'E' {
			conn.Close()
			return nil, errors.New("backend error: " + string(payload))
		}

		if msgType == 'R' {
			authType := binary.BigEndian.Uint32(payload[:4])
			if authType == 0 {
				// AuthOk!
				// We do NOT consume the AuthOk message entirely, we want to forward it to the client!
				// Wait, the client is waiting for AuthOk. We can return an AuthOk buffer to send to client.
				// But we've already consumed it from the socket.
				return &peekedConn{Conn: conn, peeked: append(header[:], payload...)}, nil
			} else if authType == 3 { // Cleartext
				conn.Close()
				return nil, errors.New("cleartext authentication is not supported")
			} else if authType == 10 { // SASL
				// Assume SCRAM-SHA-256 is supported by the server and pick it.
				// The payload contains a list of supported mechanisms, but we hardcode SCRAM-SHA-256.
				saslClient = scram.NewClient(sha256.New, db.User, db.Password)
				saslClient.Step(nil)
				clientOut := saslClient.Out()

				mech := "SCRAM-SHA-256"
				msgLen := 4 + len(mech) + 1 + 4 + len(clientOut)
				resp := make([]byte, 1+msgLen)
				resp[0] = 'p'
				binary.BigEndian.PutUint32(resp[1:5], uint32(msgLen))
				copy(resp[5:], mech)
				resp[5+len(mech)] = 0
				binary.BigEndian.PutUint32(resp[5+len(mech)+1:5+len(mech)+5], uint32(len(clientOut)))
				copy(resp[5+len(mech)+5:], clientOut)

				if _, err := conn.Write(resp); err != nil {
					conn.Close()
					return nil, err
				}
			} else if authType == 11 { // SASLContinue
				if saslClient == nil {
					conn.Close()
					return nil, errors.New("unexpected SASLContinue")
				}
				serverFirstMessage := payload[4:]
				saslClient.Step(serverFirstMessage)
				if err := saslClient.Err(); err != nil {
					conn.Close()
					return nil, err
				}
				clientOut := saslClient.Out()
				msgLen := 4 + len(clientOut)
				resp := make([]byte, 1+msgLen)
				resp[0] = 'p'
				binary.BigEndian.PutUint32(resp[1:5], uint32(msgLen))
				copy(resp[5:], clientOut)

				if _, err := conn.Write(resp); err != nil {
					conn.Close()
					return nil, err
				}
			} else if authType == 12 { // SASLFinal
				if saslClient == nil {
					conn.Close()
					return nil, errors.New("unexpected SASLFinal")
				}
				serverFinalMessage := payload[4:]
				saslClient.Step(serverFinalMessage)
				if err := saslClient.Err(); err != nil {
					conn.Close()
					return nil, err
				}
			} else if authType == 5 { // MD5
				salt := payload[4:8]
				hash := computeMD5(db.Password, db.User, salt)
				pwdMsg := make([]byte, 5+len(hash)+1)
				pwdMsg[0] = 'p'
				binary.BigEndian.PutUint32(pwdMsg[1:5], uint32(len(hash)+5))
				copy(pwdMsg[5:], hash)
				pwdMsg[len(pwdMsg)-1] = 0
				if _, err := conn.Write(pwdMsg); err != nil {
					conn.Close()
					return nil, err
				}
			} else {
				conn.Close()
				return nil, fmt.Errorf("unsupported auth type: %d", authType)
			}
		} else {
			conn.Close()
			return nil, fmt.Errorf("unexpected message type during auth: %c", msgType)
		}
	}
}

func computeMD5(password, user string, salt []byte) string {
	h := md5.New()
	h.Write([]byte(password + user))
	hash1 := hex.EncodeToString(h.Sum(nil))

	h2 := md5.New()
	h2.Write([]byte(hash1))
	h2.Write(salt)
	hash2 := hex.EncodeToString(h2.Sum(nil))

	return "md5" + hash2
}

// peekedConn wraps a net.Conn and replays peeked bytes first
type peekedConn struct {
	net.Conn
	peeked []byte
}

func (c *peekedConn) Read(b []byte) (n int, err error) {
	if len(c.peeked) > 0 {
		n = copy(b, c.peeked)
		c.peeked = c.peeked[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}
