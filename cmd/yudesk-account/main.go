package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/yudesk/yudesk/internal/session"
	"golang.org/x/term"
)

func main() {
	flags := flag.NewFlagSet("yudesk-account", flag.ExitOnError)
	server := flags.String("server", "http://127.0.0.1:9380", "relay account API")
	username := flags.String("username", "", "account username")
	password := flags.String("password", "", "account password; prefer prompt or YUDESK_PASSWORD")
	activationKey := flags.String("key", "", "activation key")
	deviceID := flags.String("device-id", "", "device ID")
	allowHTTP := flags.Bool("allow-http", false, "allow unencrypted HTTP to a non-loopback server")
	serverFingerprint := flags.String("server-fingerprint", "", "expected HTTPS server TLS SHA-256 fingerprint")
	if len(os.Args) < 2 {
		usage()
		return
	}
	action := os.Args[1]
	_ = flags.Parse(os.Args[2:])
	if err := validateServer(*server, *allowHTTP); err != nil {
		log.Fatal(err)
	}
	client, err := newHTTPClient(*server, *serverFingerprint)
	if err != nil {
		log.Fatal(err)
	}
	switch action {
	case "health":
		printResponse(doJSON(client, *server, "GET", "/healthz", "", nil))
	case "register":
		if *username == "" {
			log.Fatal("-username is required")
		}
		pass, err := readPassword(*password)
		if err != nil {
			log.Fatal(err)
		}
		printResponse(doJSON(client, *server, "POST", "/api/register", "", map[string]string{"username": *username, "password": pass}))
	case "login":
		if *username == "" {
			log.Fatal("-username is required")
		}
		pass, err := readPassword(*password)
		if err != nil {
			log.Fatal(err)
		}
		body, err := doJSON(client, *server, "POST", "/api/login", "", map[string]string{"username": *username, "password": pass})
		if err != nil {
			log.Fatal(err)
		}
		var result struct {
			OK          bool      `json:"ok"`
			Token       string    `json:"token"`
			Error       string    `json:"error"`
			ActiveUntil time.Time `json:"activeUntil"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			log.Fatal(err)
		}
		if !result.OK {
			log.Fatal(result.Error)
		}
		if err := session.Save(result.Token); err != nil {
			log.Fatal(err)
		}
		if result.ActiveUntil.After(time.Now()) {
			fmt.Printf("登录成功，会话令牌已安全保存；授权有效期至 %s\n", result.ActiveUntil.Local().Format(time.RFC3339))
		} else {
			fmt.Println("登录成功，会话令牌已安全保存；账号尚未激活或授权已过期")
		}
	case "activate":
		if *activationKey == "" {
			log.Fatal("-key is required")
		}
		printResponse(doAuthorized(client, *server, "POST", "/api/activation/redeem", map[string]string{"key": *activationKey}))
	case "status":
		printResponse(doAuthorized(client, *server, "GET", "/api/license", nil))
	case "devices":
		printResponse(doAuthorized(client, *server, "GET", "/api/devices", nil))
	case "audit":
		printResponse(doAuthorized(client, *server, "GET", "/api/audit", nil))
	case "revoke-device":
		if *deviceID == "" {
			log.Fatal("-device-id is required")
		}
		printResponse(doAuthorized(client, *server, "POST", "/api/devices/revoke", map[string]string{"id": *deviceID}))
	case "logout":
		_, err := doAuthorized(client, *server, "POST", "/api/logout", nil)
		if err != nil {
			log.Print(err)
		}
		if err := session.Clear(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("已退出登录")
	default:
		usage()
	}
}

func usage() {
	fmt.Println("用法: yudesk-account health|register|login|activate|status|devices|audit|revoke-device|logout [参数]")
}
func readPassword(value string) (string, error) {
	if value != "" {
		return value, nil
	}
	if value = os.Getenv("YUDESK_PASSWORD"); value != "" {
		return value, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("password required via prompt or YUDESK_PASSWORD")
	}
	fmt.Print("密码: ")
	data, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return string(data), err
}
func validateServer(raw string, allowHTTP bool) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return errors.New("server URL must use http or https")
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	if !allowHTTP {
		return errors.New("refusing to send credentials over public HTTP; use HTTPS or -allow-http")
	}
	return nil
}

func newHTTPClient(raw, fingerprint string) (*http.Client, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	want := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(fingerprint), ":", ""))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if want == "" {
		return &http.Client{Transport: transport, Timeout: 15 * time.Second}, nil
	}
	if parsed.Scheme != "https" {
		return nil, errors.New("-server-fingerprint requires an HTTPS server URL")
	}
	expected, err := hex.DecodeString(want)
	if err != nil || len(expected) != sha256.Size {
		return nil, errors.New("server fingerprint must be a 64-character SHA-256 hexadecimal value")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true} // Identity is verified by the pinned certificate below.
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("server sent no certificate")
		}
		got := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(got[:], expected) != 1 {
			return errors.New("server certificate fingerprint mismatch")
		}
		return nil
	}
	transport.TLSClientConfig = config
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}, nil
}

func doAuthorized(client *http.Client, server, method, path string, payload any) ([]byte, error) {
	token, err := session.Load()
	if err != nil {
		return nil, errors.New("please login first")
	}
	return doJSON(client, server, method, path, token, payload)
}
func doJSON(client *http.Client, server, method, path, token string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, strings.TrimRight(server, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("server returned %s: %s", response.Status, string(data))
	}
	return data, nil
}
func printResponse(data []byte, err error) {
	if err != nil {
		log.Fatal(err)
	}
	var value any
	if json.Unmarshal(data, &value) == nil {
		pretty, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(pretty))
		return
	}
	fmt.Println(string(data))
}
