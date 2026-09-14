package relay

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

type Hello struct {
	Role      string `json:"role"`
	ID        string `json:"id"`
	Room      string `json:"room,omitempty"`
	Name      string `json:"name,omitempty"`
	Topic     string `json:"topic,omitempty"`
	PIN       string `json:"pin,omitempty"`
	Action    string `json:"action,omitempty"`
	Token     string `json:"token"`
	Auth      string `json:"auth,omitempty"`
	PublicKey []byte `json:"publicKey,omitempty"`
	// PeerPathV2 advertises support for the authenticated ICE policy returned
	// on the final OK line. Older relays ignore it and older clients never
	// receive the extended response, preserving wire compatibility.
	PeerPathV2 bool `json:"peerPathV2,omitempty"`
}

type DialOptions struct {
	TLS         bool
	Insecure    bool
	CAFile      string
	Fingerprint string
}

// RejectionError is a structured refusal returned by the relay before a
// transparent session is established.
type RejectionError struct {
	Code    string
	Message string
}

func (e *RejectionError) Error() string {
	if e.Message == "" {
		return "relay rejected connection: " + e.Code
	}
	return "relay rejected connection: " + e.Code + ": " + e.Message
}

func RejectionCode(err error) string {
	var rejection *RejectionError
	if errors.As(err, &rejection) {
		return rejection.Code
	}
	return ""
}

// Dial establishes an outbound connection to the relay. After the short hello
// exchange, the returned connection is a transparent byte pipe.
func Dial(addr string, useTLS bool, h Hello) (net.Conn, error) {
	return DialWithOptions(addr, DialOptions{TLS: useTLS, Insecure: useTLS}, h)
}

func DialWithOptions(addr string, options DialOptions, h Hello) (net.Conn, error) {
	return DialWithContext(context.Background(), addr, options, h)
}

func dialTransport(ctx context.Context, addr string, options DialOptions) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if options.TLS {
		config, configErr := clientTLSConfig(addr, options)
		if configErr != nil {
			return nil, configErr
		}
		return (&tls.Dialer{NetDialer: &d, Config: config}).DialContext(ctx, "tcp", addr)
	}
	return d.DialContext(ctx, "tcp", addr)
}

// Cancellation applies both while dialing and while waiting for a peer.
func DialWithContext(ctx context.Context, addr string, options DialOptions, h Hello) (net.Conn, error) {
	c, err := dialTransport(ctx, addr, options)
	if err != nil {
		return nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCancel()
	b, _ := json.Marshal(h)
	if _, err = fmt.Fprintf(c, "YU_RELAY/1 %s\n", b); err != nil {
		c.Close()
		return nil, err
	}
	reader := bufio.NewReaderSize(c, 16<<10)
	if h.Role == "viewer" {
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			c.Close()
			return nil, readErr
		}
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "WAIT", "PING":
			continue
		case "OK":
			_ = c.SetReadDeadline(time.Time{})
			return relayResultConn(c, reader, nil), nil
		default:
			if h.PeerPathV2 && strings.HasPrefix(trimmed, "OK ") {
				policy, policyErr := decodeRTCPolicy(strings.TrimSpace(strings.TrimPrefix(trimmed, "OK ")))
				if policyErr != nil {
					c.Close()
					return nil, policyErr
				}
				_ = c.SetReadDeadline(time.Time{})
				return relayResultConn(c, reader, policy), nil
			}
			c.Close()
			return nil, parseRelayRejection(trimmed)
		}
	}
}

const maxRTCPolicyBytes = 8 << 10

// EncodeRTCPolicy serializes the temporary, session-bound ICE policy carried
// by an extended relay OK response. It is exported for the relay server; the
// result is safe on one line and contains no long-lived secret.
func EncodeRTCPolicy(policy *RTCPolicy) (string, error) {
	if err := validateRTCPolicy(policy); err != nil {
		return "", err
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	if len(raw) > maxRTCPolicyBytes {
		return "", errors.New("relay RTC policy is too large")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeRTCPolicy(encoded string) (*RTCPolicy, error) {
	if len(encoded) == 0 || len(encoded) > base64.RawURLEncoding.EncodedLen(maxRTCPolicyBytes) {
		return nil, errors.New("invalid relay RTC policy size")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > maxRTCPolicyBytes {
		return nil, errors.New("invalid relay RTC policy encoding")
	}
	var policy RTCPolicy
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&policy); err != nil {
		return nil, errors.New("invalid relay RTC policy")
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid trailing relay RTC policy data")
	}
	if err = validateRTCPolicy(&policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func validateRTCPolicy(policy *RTCPolicy) error {
	if policy == nil || policy.DirectTimeoutMS < 500 || policy.DirectTimeoutMS > 10000 || len(policy.ICEServers) == 0 || len(policy.ICEServers) > 8 {
		return errors.New("invalid relay RTC policy")
	}
	for _, server := range policy.ICEServers {
		if len(server.URLs) == 0 || len(server.URLs) > 4 || len(server.Username) > 512 || len(server.Credential) > 1024 {
			return errors.New("invalid relay RTC server")
		}
		for _, value := range server.URLs {
			if len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\r\n") {
				return errors.New("invalid relay RTC URL")
			}
		}
	}
	return nil
}

type rtcPolicyConn struct {
	net.Conn
	policy *RTCPolicy
}

func relayResultConn(conn net.Conn, reader *bufio.Reader, policy *RTCPolicy) net.Conn {
	if reader.Buffered() > 0 {
		conn = &bufferedConn{Conn: conn, reader: reader}
	}
	if policy != nil {
		return &rtcPolicyConn{Conn: conn, policy: policy}
	}
	return conn
}

// PeerRTCPolicy returns a defensive copy of the authenticated, per-session
// ICE policy attached by a compatible relay. Call it before wrapping conn.
func PeerRTCPolicy(conn net.Conn) *RTCPolicy {
	provider, ok := conn.(interface{ peerRTCPolicy() *RTCPolicy })
	if !ok {
		return nil
	}
	policy := provider.peerRTCPolicy()
	if policy == nil {
		return nil
	}
	raw, _ := json.Marshal(policy)
	var copy RTCPolicy
	_ = json.Unmarshal(raw, &copy)
	return &copy
}

func (c *rtcPolicyConn) peerRTCPolicy() *RTCPolicy { return c.policy }

func parseRelayRejection(line string) error {
	if !strings.HasPrefix(line, "ERR ") {
		return errors.New(line)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "ERR "))
	parts := strings.SplitN(rest, " ", 2)
	code := "DENIED"
	message := rest
	if len(parts) == 2 && validRejectionCode(parts[0]) {
		code = parts[0]
		message = strings.TrimSpace(parts[1])
	} else {
		lower := strings.ToLower(rest)
		switch {
		case strings.Contains(lower, "activation is required") || strings.Contains(lower, "has expired"):
			code = "LICENSE_REQUIRED"
		case strings.Contains(lower, "revoked") || strings.Contains(lower, "disabled"):
			code = "DISABLED"
		}
	}
	return &RejectionError{Code: code, Message: message}
}

func validRejectionCode(code string) bool {
	if code == "" {
		return false
	}
	for _, char := range code {
		if (char < 'A' || char > 'Z') && char != '_' {
			return false
		}
	}
	return true
}

func clientTLSConfig(addr string, options DialOptions) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host}
	if options.CAFile != "" {
		pemData, readErr := os.ReadFile(options.CAFile)
		if readErr != nil {
			return nil, readErr
		}
		pool, rootErr := x509.SystemCertPool()
		if rootErr != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, errors.New("relay CA file contains no certificates")
		}
		config.RootCAs = pool
	}
	want := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(options.Fingerprint), ":", ""))
	if want != "" {
		config.InsecureSkipVerify = true
		config.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("relay sent no certificate")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			got := strings.ToUpper(hex.EncodeToString(sum[:]))
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return errors.New("relay certificate fingerprint mismatch")
			}
			return nil
		}
	}
	if options.Insecure {
		config.InsecureSkipVerify = true
		config.VerifyConnection = nil
	}
	return config, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func Proxy(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); _ = a.Close(); _ = b.Close(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); _ = a.Close(); _ = b.Close(); done <- struct{}{} }()
	<-done
}
