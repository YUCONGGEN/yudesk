package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
)

const (
	portMapMin   = 9000
	portMapMax   = 9500
	portMapLimit = 5
)

type serverPortMap struct {
	relay.PortMap
	DeviceID   string
	DeviceCode string
	DeviceName string
	RequestID  string
	Session    string
	LastSeen   time.Time
	HookReady  bool
}

type adminPortMapView struct {
	ID, DeviceID, DeviceCode, DeviceName, Name, Protocol string
	LocalPort, RemotePort                                int
	PublicAddress, Status, StatusClass, CreatedAt, Seen  string
}

func randomURLToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (b *broker) createPortMap(deviceID, session string, command relay.PortMapCommand) relay.PortMapResult {
	result := relay.PortMapResult{RequestID: command.RequestID}
	name := strings.TrimSpace(command.Name)
	if command.RequestID == "" {
		result.Message = "请求编号无效"
		return result
	}
	if command.LocalPort < 1 || command.LocalPort > 65535 {
		result.Message = "本地端口必须在 1–65535 之间"
		return result
	}
	if name == "" {
		name = "本地端口 " + strconv.Itoa(command.LocalPort)
	}
	if len([]rune(name)) > 40 {
		result.Message = "备注不能超过 40 个字符"
		return result
	}
	expiry, err := b.accounts.DeviceLicenseExpiry(deviceID)
	if err != nil || !expiry.After(time.Now()) {
		result.Message = "设备未激活或已被禁用"
		return result
	}
	secret, err := randomURLToken(32)
	if err != nil {
		result.Message = "无法生成安全映射凭据"
		return result
	}
	mapID, err := randomURLToken(12)
	if err != nil {
		result.Message = "无法生成映射编号"
		return result
	}
	deviceName, deviceCode := b.accounts.DevicePublicLabel(deviceID)

	b.Lock()
	if b.portMaps == nil {
		b.portMaps = make(map[string]*serverPortMap)
	}
	if b.portByNumber == nil {
		b.portByNumber = make(map[int]string)
	}
	if b.portMapServer == "" {
		b.Unlock()
		result.Message = "服务器尚未启用端口映射"
		return result
	}
	control := b.controls[deviceID]
	if control == nil || control.stopping || !constantStringEqual(control.portMapSession, session) {
		b.Unlock()
		result.Message = "设备管理连接已失效，请稍后重试"
		return result
	}
	count := 0
	for _, mapping := range b.portMaps {
		if mapping.DeviceID != deviceID {
			continue
		}
		count++
		if mapping.RequestID == command.RequestID {
			b.Unlock()
			result.OK = true
			result.Message = "映射已创建"
			return result
		}
	}
	if count >= portMapLimit {
		b.Unlock()
		result.Message = "每台设备最多同时开启 5 个端口映射"
		return result
	}
	remotePort, err := b.randomFreePortLocked()
	if err != nil {
		b.Unlock()
		result.Message = err.Error()
		return result
	}
	publicHost, _, splitErr := net.SplitHostPort(b.portMapServer)
	if splitErr != nil {
		b.Unlock()
		result.Message = "服务器端口映射地址配置无效"
		return result
	}
	publicAddress := net.JoinHostPort(publicHost, strconv.Itoa(remotePort))
	now := time.Now()
	mapping := &serverPortMap{
		PortMap: relay.PortMap{
			ID: mapID, Name: name, Protocol: "tcp", LocalPort: command.LocalPort,
			RemotePort: remotePort, PublicAddress: publicAddress, Secret: secret, CreatedAt: now,
		},
		DeviceID: deviceID, DeviceCode: deviceCode, DeviceName: deviceName,
		RequestID: command.RequestID, Session: session, LastSeen: now,
	}
	b.portMaps[mapID] = mapping
	b.portByNumber[remotePort] = mapID
	b.Unlock()

	if hookErr := b.runPortMapHook("open", remotePort); hookErr != nil {
		fmt.Printf("cannot open managed port %d: %v\n", remotePort, hookErr)
		b.Lock()
		if b.portMaps[mapID] == mapping {
			b.removePortMapLocked(mapID)
		}
		b.Unlock()
		if closeErr := b.runPortMapHook("close", remotePort); closeErr != nil {
			fmt.Printf("cannot clean a failed managed port %d: %v\n", remotePort, closeErr)
		}
		result.Message = "服务器无法开放公网端口，请稍后重试"
		return result
	}

	// A device can disconnect while the router is opening the port. Commit the
	// hook only if this exact mapping and authenticated control session remain.
	b.Lock()
	control = b.controls[deviceID]
	current := b.portMaps[mapID]
	committed := current == mapping && control != nil && !control.stopping && constantStringEqual(control.portMapSession, session)
	if committed {
		mapping.HookReady = true
	} else if current == mapping {
		b.removePortMapLocked(mapID)
	}
	b.Unlock()
	if !committed {
		if closeErr := b.runPortMapHook("close", remotePort); closeErr != nil {
			fmt.Printf("cannot close stale managed port %d: %v\n", remotePort, closeErr)
		}
		result.Message = "设备已离线，公网端口已回收"
		return result
	}

	result.OK = true
	result.Message = "映射已创建：" + publicAddress
	b.accounts.Audit(account.AuditEntry{Action: "port_map_created", DeviceID: deviceID, Detail: fmt.Sprintf("tcp %s -> 127.0.0.1:%d", publicAddress, command.LocalPort)})
	return result
}

func (b *broker) randomFreePortLocked() (int, error) {
	span := int64(portMapMax - portMapMin + 1)
	start, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return 0, err
	}
	for offset := int64(0); offset < span; offset++ {
		port := portMapMin + int((start.Int64()+offset)%span)
		if _, used := b.portByNumber[port]; !used {
			return port, nil
		}
	}
	return 0, errors.New("9000–9500 端口池暂时已满")
}

func (b *broker) deletePortMap(deviceID string, command relay.PortMapCommand) relay.PortMapResult {
	result := relay.PortMapResult{RequestID: command.RequestID}
	b.Lock()
	mapping := b.portMaps[command.MapID]
	if mapping == nil || mapping.DeviceID != deviceID {
		b.Unlock()
		result.Message = "映射不存在或已经关闭"
		return result
	}
	b.removePortMapLocked(mapping.ID)
	b.Unlock()
	if hookErr := b.runPortMapHook("close", mapping.RemotePort); hookErr != nil {
		fmt.Printf("cannot close managed port %d: %v\n", mapping.RemotePort, hookErr)
	}
	b.accounts.Audit(account.AuditEntry{Action: "port_map_closed", DeviceID: deviceID, Detail: fmt.Sprintf("tcp %s -> 127.0.0.1:%d", mapping.PublicAddress, mapping.LocalPort)})
	result.OK = true
	result.Message = "端口映射已关闭"
	return result
}

func (b *broker) processPortMapCommand(deviceID, session string, command *relay.PortMapCommand) relay.PortMapResult {
	if command == nil {
		return relay.PortMapResult{}
	}
	switch strings.ToLower(strings.TrimSpace(command.Action)) {
	case "create":
		return b.createPortMap(deviceID, session, *command)
	case "delete":
		return b.deletePortMap(deviceID, *command)
	default:
		return relay.PortMapResult{RequestID: command.RequestID, Message: "不支持的端口映射操作"}
	}
}

func (b *broker) removePortMapLocked(mapID string) *serverPortMap {
	mapping := b.portMaps[mapID]
	if mapping == nil {
		return nil
	}
	delete(b.portMaps, mapID)
	delete(b.portByNumber, mapping.RemotePort)
	return mapping
}

func (b *broker) closeDevicePortMaps(deviceID string) {
	var removed []*serverPortMap
	b.Lock()
	for mapID, mapping := range b.portMaps {
		if mapping.DeviceID == deviceID {
			if closed := b.removePortMapLocked(mapID); closed != nil {
				removed = append(removed, closed)
			}
		}
	}
	b.Unlock()
	b.closePortMapHooks(removed)
}

func (b *broker) closePortMapByAdmin(mapID string) *serverPortMap {
	b.Lock()
	mapping := b.removePortMapLocked(mapID)
	b.Unlock()
	if mapping != nil {
		b.closePortMapHooks([]*serverPortMap{mapping})
	}
	return mapping
}

func (b *broker) portMapStatus(deviceID, session string) []relay.PortMap {
	b.Lock()
	defer b.Unlock()
	maps := make([]relay.PortMap, 0, portMapLimit)
	for _, mapping := range b.portMaps {
		if mapping.DeviceID == deviceID && mapping.HookReady && constantStringEqual(mapping.Session, session) {
			maps = append(maps, mapping.PortMap)
		}
	}
	sort.Slice(maps, func(i, j int) bool { return maps[i].RemotePort < maps[j].RemotePort })
	return maps
}

func (b *broker) adminPortMaps() []adminPortMapView {
	b.Lock()
	defer b.Unlock()
	result := make([]adminPortMapView, 0, len(b.portMaps))
	for _, mapping := range b.portMaps {
		status, class := "开放公网端口", "waiting"
		if mapping.Online {
			status, class = "映射中", "online"
		}
		result = append(result, adminPortMapView{
			ID: mapping.ID, DeviceID: mapping.DeviceID, DeviceCode: mapping.DeviceCode,
			DeviceName: mapping.DeviceName, Name: mapping.Name, Protocol: strings.ToUpper(mapping.Protocol),
			LocalPort: mapping.LocalPort, RemotePort: mapping.RemotePort, PublicAddress: mapping.PublicAddress,
			Status: status, StatusClass: class, CreatedAt: formatAdminTime(mapping.CreatedAt), Seen: formatAdminTime(mapping.LastSeen),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RemotePort < result[j].RemotePort })
	return result
}

func (b *broker) runPortMapHook(action string, port int) error {
	if b.portMapHook == "" {
		return nil
	}
	if action != "open" && action != "close" && action != "cleanup" {
		return errors.New("invalid port hook action")
	}
	b.portMapHookMu.Lock()
	defer b.portMapHookMu.Unlock()
	arguments := []string{action}
	if action != "cleanup" {
		if port < portMapMin || port > portMapMax {
			return errors.New("port hook received a port outside 9000-9500")
		}
		arguments = append(arguments, strconv.Itoa(port))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	output, err := portMapHookCommandContext(ctx, b.portMapHook, arguments...).CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("%s timed out", action)
	}
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 512 {
			message = message[:512]
		}
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s failed: %s", action, message)
	}
	return nil
}

func (b *broker) cleanupPortMapPorts() error {
	return b.runPortMapHook("cleanup", 0)
}

func (b *broker) closePortMapHooks(mappings []*serverPortMap) {
	for _, mapping := range mappings {
		if mapping == nil {
			continue
		}
		if err := b.runPortMapHook("close", mapping.RemotePort); err != nil {
			fmt.Printf("cannot close managed port %d: %v\n", mapping.RemotePort, err)
		}
	}
}

func constantStringEqual(a, b string) bool {
	left, right := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

type frpPluginRequest struct {
	Version string          `json:"version"`
	Op      string          `json:"op"`
	Content json.RawMessage `json:"content"`
}

type frpPluginUser struct {
	User  string            `json:"user"`
	Metas map[string]string `json:"metas"`
}

type frpPluginLogin struct {
	User  string            `json:"user"`
	Metas map[string]string `json:"metas"`
}

type frpPluginProxy struct {
	User       frpPluginUser     `json:"user"`
	ProxyName  string            `json:"proxy_name"`
	ProxyType  string            `json:"proxy_type"`
	RemotePort int               `json:"remote_port"`
	Metas      map[string]string `json:"metas"`
}

type frpPluginUserConnection struct {
	User      frpPluginUser `json:"user"`
	ProxyName string        `json:"proxy_name"`
	ProxyType string        `json:"proxy_type"`
}

type frpPluginResponse struct {
	Reject       bool   `json:"reject"`
	RejectReason string `json:"reject_reason,omitempty"`
	Unchange     bool   `json:"unchange"`
}

func expectedFRPProxyName(deviceID, mapID string) string {
	return strings.ToUpper(strings.TrimSpace(deviceID)) + ".yudesk-" + mapID
}

func frpMapID(deviceID, proxyName string) string {
	return strings.TrimPrefix(proxyName, strings.ToUpper(strings.TrimSpace(deviceID))+".yudesk-")
}

func (b *broker) validFRPSessionLocked(deviceID string, metas map[string]string) bool {
	control := b.controls[strings.ToUpper(strings.TrimSpace(deviceID))]
	return control != nil && !control.stopping && constantStringEqual(control.portMapSession, metas["yudesk_session"])
}

func (b *broker) authorizeFRPRequest(request frpPluginRequest) (bool, string) {
	b.Lock()
	defer b.Unlock()
	reject := func(reason string) (bool, string) { return false, reason }
	switch request.Op {
	case "Login":
		var content frpPluginLogin
		if json.Unmarshal(request.Content, &content) != nil || !b.validFRPSessionLocked(content.User, content.Metas) {
			return reject("YuDesk device session is offline or invalid")
		}
		for _, mapping := range b.portMaps {
			if mapping.DeviceID == content.User && constantStringEqual(mapping.Session, content.Metas["yudesk_session"]) {
				return true, ""
			}
		}
		return reject("YuDesk device has no active port mappings")
	case "NewProxy":
		var content frpPluginProxy
		if json.Unmarshal(request.Content, &content) != nil || !b.validFRPSessionLocked(content.User.User, content.User.Metas) {
			return reject("invalid YuDesk device session")
		}
		mapID, secret := content.Metas["yudesk_map_id"], content.Metas["yudesk_map_secret"]
		mapping := b.portMaps[mapID]
		if mapping == nil || mapping.DeviceID != content.User.User || !constantStringEqual(mapping.Session, content.User.Metas["yudesk_session"]) || !constantStringEqual(mapping.Secret, secret) || content.ProxyName != expectedFRPProxyName(content.User.User, mapID) || content.ProxyType != "tcp" || content.RemotePort != mapping.RemotePort || content.RemotePort < portMapMin || content.RemotePort > portMapMax {
			return reject("port mapping was not issued by YuDesk")
		}
		mapping.Online = true
		mapping.LastSeen = time.Now()
		return true, ""
	case "CloseProxy":
		var content frpPluginProxy
		if json.Unmarshal(request.Content, &content) != nil || !b.validFRPSessionLocked(content.User.User, content.User.Metas) {
			return reject("invalid YuDesk device session")
		}
		mapID := frpMapID(content.User.User, content.ProxyName)
		if mapping := b.portMaps[mapID]; mapping != nil && mapping.DeviceID == content.User.User {
			mapping.Online = false
			mapping.LastSeen = time.Now()
		}
		return true, ""
	case "Ping", "NewWorkConn":
		var content struct {
			User frpPluginUser `json:"user"`
		}
		if json.Unmarshal(request.Content, &content) != nil || !b.validFRPSessionLocked(content.User.User, content.User.Metas) {
			return reject("invalid YuDesk device session")
		}
		for _, mapping := range b.portMaps {
			if mapping.DeviceID == content.User.User {
				mapping.LastSeen = time.Now()
			}
		}
		return true, ""
	case "NewUserConn":
		var content frpPluginUserConnection
		if json.Unmarshal(request.Content, &content) != nil || !b.validFRPSessionLocked(content.User.User, content.User.Metas) {
			return reject("invalid YuDesk device session")
		}
		mapID := frpMapID(content.User.User, content.ProxyName)
		mapping := b.portMaps[mapID]
		if mapping == nil || mapping.DeviceID != content.User.User || content.ProxyName != expectedFRPProxyName(content.User.User, mapID) || content.ProxyType != "tcp" {
			return reject("port mapping has been closed")
		}
		mapping.LastSeen = time.Now()
		return true, ""
	default:
		return reject("unsupported FRP operation")
	}
}

func (b *broker) serveFRPPlugin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(remoteHost) == nil || !net.ParseIP(remoteHost).IsLoopback() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var request frpPluginRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if decoder.Decode(&request) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ok, reason := b.authorizeFRPRequest(request)
	_ = json.NewEncoder(w).Encode(frpPluginResponse{Reject: !ok, RejectReason: reason, Unchange: true})
}

func startFRPPluginServer(b *broker, address string) (*http.Server, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid FRP plugin address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("FRP plugin must listen on a loopback address")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/frp", b.serveFRPPlugin)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("FRP plugin server stopped: %v\n", err)
		}
	}()
	return server, nil
}
