package relay

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yudesk/yudesk/internal/security"
)

func TestResolutionRequiresPinnedIdentityAndMatchingCode(t *testing.T) {
	dir := t.TempDir()
	cfg, fingerprint, err := security.LoadOrCreateServerConfig(filepath.Join(dir, "cert"), filepath.Join(dir, "key"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, code, pin, response string
		insecure, wantOK          bool
	}{
		{"valid", "123456789", fingerprint, `{"code":"123456789","id":"ABCDEF0123456789ABCDEF01"}`, false, true},
		{"wrong certificate", "123456789", strings.Repeat("0", 64), `{}`, false, false},
		{"wrong code", "123456789", fingerprint, `{"code":"987654321","id":"ABCDEF0123456789ABCDEF01"}`, false, false},
		{"invalid identity", "123456789", fingerprint, `{"code":"123456789","id":"not-an-identity"}`, false, false},
		{"insecure", "123456789", fingerprint, `{}`, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				var h Hello
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "YU_RELAY/1 ")), &h) != nil || h.Role != "resolve" {
					return
				}
				_, _ = c.Write([]byte(test.response + "\n"))
			}()
			_, err = ResolveDevice(context.Background(), ln.Addr().String(), DialOptions{TLS: true, Fingerprint: test.pin, Insecure: test.insecure}, test.code)
			if (err == nil) != test.wantOK {
				t.Fatalf("resolution success=%t want=%t", err == nil, test.wantOK)
			}
			ln.Close()
			<-done
		})
	}
}
