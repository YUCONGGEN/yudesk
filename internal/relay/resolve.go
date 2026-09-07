package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Resolution struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

func IsDeviceCode(value string) bool {
	if len(value) != 9 || value[0] == '0' {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Resolution uses the pinned TLS relay, never the public HTTP website. The
// returned full identity is subsequently verified by the end-to-end handshake.
func ResolveDevice(ctx context.Context, addr string, options DialOptions, code string) (Resolution, error) {
	var result Resolution
	if !IsDeviceCode(code) {
		return result, errors.New("设备码应为 9 位数字")
	}
	if !options.TLS || options.Insecure {
		return result, errors.New("短设备码解析要求已验证的 TLS 中转连接")
	}
	c, err := dialTransport(ctx, addr, options)
	if err != nil {
		return result, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	hello, _ := json.Marshal(Hello{Role: "resolve", ID: code})
	if _, err = fmt.Fprintf(c, "YU_RELAY/1 %s\n", hello); err != nil {
		return result, err
	}
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 1024), 16384)
	if !scanner.Scan() {
		return result, errors.New("服务器不支持短设备码或暂时不可用")
	}
	if json.Unmarshal(scanner.Bytes(), &result) != nil || result.Error != "" || result.Code != code || len(result.ID) != 24 || strings.Trim(result.ID, "0123456789ABCDEF") != "" {
		return Resolution{}, errors.New("未找到设备或设备码解析失败")
	}
	return result, nil
}
