package main

// Conference TURN forwards encrypted WebRTC packets only when direct ICE
// cannot connect. DTLS/SRTP remains terminated by the two meeting clients.
import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v5"
	"github.com/yudesk/yudesk/internal/relay"
	"golang.org/x/time/rate"
)

const (
	conferenceTURNCredentialLifetime = 2 * time.Hour
	conferenceTURNAllocationLifetime = 10 * time.Minute
	conferenceTURNMaxAllocations     = 20
	conferenceTURNBytesPerSecond     = 8 << 20
)

type conferenceTURNConfig struct {
	Listen, PublicAddress, SecretFile, Realm string
	RelayMinPort, RelayMaxPort               int
	AllowedPeerCIDRs                         []string
}

type conferenceTURN struct {
	server      *turn.Server
	address     string
	owner       *broker
	config      conferenceTURNConfig
	secret      []byte
	generator   *turn.RelayAddressGeneratorPortRange
	allowed     []*net.IPNet
	mu          sync.Mutex
	closing     bool
	connections map[*conferencePacketConn]bool
	users       map[string]*conferenceTURNUser
	done        chan struct{}
	once        sync.Once
}

type conferenceTURNUser struct {
	count   int
	limiter *rate.Limiter
}

type conferencePacketConn struct {
	net.PacketConn
	relay          *conferenceTURN
	username, user string
	limiter        *rate.Limiter
	once           sync.Once
}

func startConferenceTURN(owner *broker, config conferenceTURNConfig) (*conferenceTURN, error) {
	if owner == nil || config.Listen == "" || config.PublicAddress == "" || config.SecretFile == "" || config.Realm == "" ||
		config.RelayMinPort < 1024 || config.RelayMaxPort > 65534 || config.RelayMinPort > config.RelayMaxPort || config.RelayMaxPort-config.RelayMinPort > 255 {
		return nil, errors.New("invalid conference TURN configuration")
	}
	_, listenPort, err := net.SplitHostPort(config.Listen)
	if err != nil {
		return nil, errors.New("invalid conference TURN listen address")
	}
	port, err := strconv.Atoi(listenPort)
	if err != nil || port < 0 || port > 65535 || port >= config.RelayMinPort && port <= config.RelayMaxPort {
		return nil, errors.New("conference TURN listener overlaps relay port range")
	}
	publicHost, publicPort, err := net.SplitHostPort(config.PublicAddress)
	if err != nil || publicHost == "" || publicPort == "" {
		return nil, errors.New("invalid conference TURN public address")
	}
	secret, err := loadOrCreateTURNSecret(config.SecretFile)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", publicHost)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("conference TURN public host has no IPv4 address")
	}
	udp, err := net.ListenPacket("udp4", config.Listen)
	if err != nil {
		return nil, fmt.Errorf("conference TURN UDP listener: %w", err)
	}
	tcp, err := net.Listen("tcp4", udp.LocalAddr().String())
	if err != nil {
		udp.Close()
		return nil, fmt.Errorf("conference TURN TCP listener: %w", err)
	}
	m := &conferenceTURN{
		owner: owner, config: config, secret: secret,
		generator:   &turn.RelayAddressGeneratorPortRange{RelayAddress: ips[0], Address: "0.0.0.0", MinPort: uint16(config.RelayMinPort), MaxPort: uint16(config.RelayMaxPort), MaxRetries: 128},
		connections: map[*conferencePacketConn]bool{}, users: map[string]*conferenceTURNUser{}, done: make(chan struct{}),
	}
	for _, raw := range config.AllowedPeerCIDRs {
		_, network, parseErr := net.ParseCIDR(raw)
		if parseErr != nil {
			udp.Close()
			tcp.Close()
			return nil, errors.New("invalid conference TURN allowed peer network")
		}
		m.allowed = append(m.allowed, network)
	}
	m.server, err = turn.NewServer(turn.ServerConfig{
		Realm: config.Realm, AuthHandler: m.authenticate, AllocationLifetime: conferenceTURNAllocationLifetime,
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: m, PermissionHandler: m.permission}},
		ListenerConfigs:   []turn.ListenerConfig{{Listener: tcp, RelayAddressGenerator: m, PermissionHandler: m.permission}},
	})
	if err != nil {
		udp.Close()
		tcp.Close()
		return nil, fmt.Errorf("conference TURN startup: %w", err)
	}
	m.address = udp.LocalAddr().String()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.prune()
			case <-m.done:
				return
			}
		}
	}()
	return m, nil
}

func loadOrCreateTURNSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		raw := make([]byte, 48)
		if _, err = rand.Read(raw); err != nil {
			return nil, err
		}
		data = []byte(base64.RawURLEncoding.EncodeToString(raw))
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) < 32 || len(data) > 4096 {
		return nil, errors.New("conference TURN secret must be 32-4096 bytes")
	}
	return data, nil
}

func (m *conferenceTURN) password(username string) string {
	h := hmac.New(sha256.New, m.secret)
	h.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

type turnIdentity struct {
	kind, scope, participant, session, role string
}

func (m *conferenceTURN) credentialIdentity(username string) (turnIdentity, bool) {
	parts := strings.Split(username, ":")
	now := time.Now().Unix()
	if len(parts) < 3 {
		return turnIdentity{}, false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expires <= now || expires > now+int64(conferenceTURNCredentialLifetime/time.Second)+60 {
		return turnIdentity{}, false
	}
	if len(parts) == 3 && relay.IsMeetingCode(parts[1]) && len(parts[2]) == 24 && strings.Trim(strings.ToLower(parts[2]), "0123456789abcdef") == "" {
		return turnIdentity{kind: "conference", scope: parts[1], participant: strings.ToLower(parts[2])}, true
	}
	if len(parts) == 5 && parts[1] == "remote" && len(parts[2]) == 24 && strings.Trim(strings.ToLower(parts[2]), "0123456789abcdef") == "" &&
		len(parts[3]) == 32 && strings.Trim(strings.ToLower(parts[3]), "0123456789abcdef") == "" && (parts[4] == "agent" || parts[4] == "viewer") {
		return turnIdentity{kind: "remote", scope: strings.ToUpper(parts[2]), participant: parts[4], session: strings.ToLower(parts[3]), role: parts[4]}, true
	}
	return turnIdentity{}, false
}

func (m *conferenceTURN) identity(username string) (room, participant string, ok bool) {
	identity, valid := m.credentialIdentity(username)
	if !valid {
		return "", "", false
	}
	return identity.scope, identity.participant, true
}

func (m *conferenceTURN) alive(username string) bool {
	identity, valid := m.credentialIdentity(username)
	if !valid {
		return false
	}
	m.owner.Lock()
	defer m.owner.Unlock()
	if identity.kind == "remote" {
		session, exists := m.owner.active[identity.scope]
		return exists && session.id == identity.session && ((identity.role == "agent" && session.agent != nil) || (identity.role == "viewer" && session.viewer != nil))
	}
	room, exists := m.owner.meetings[identity.scope]
	if !exists || room.conference == nil || !room.expires.After(time.Now()) {
		return false
	}
	participant := room.conference.participants[identity.participant]
	if participant == nil {
		return false
	}
	select {
	case <-participant.done:
		return false
	default:
		return true
	}
}

func appendUniqueICEServer(servers []relay.ICEServer, server relay.ICEServer) []relay.ICEServer {
	for _, existing := range servers {
		if len(existing.URLs) == len(server.URLs) && strings.Join(existing.URLs, "\x00") == strings.Join(server.URLs, "\x00") && existing.Username == server.Username {
			return servers
		}
	}
	return append(servers, server)
}

func (m *conferenceTURN) policy(room, participant string, roomExpiry time.Time, stunURL string, directTimeoutMS int) *relay.RTCPolicy {
	policy := &relay.RTCPolicy{ICEServers: []relay.ICEServer{}, DirectTimeoutMS: directTimeoutMS}
	if strings.HasPrefix(stunURL, "stun:") {
		policy.ICEServers = append(policy.ICEServers, relay.ICEServer{URLs: []string{stunURL}})
	}
	expires := time.Now().Add(conferenceTURNCredentialLifetime)
	if roomExpiry.Before(expires) {
		expires = roomExpiry
	}
	username := fmt.Sprintf("%d:%s:%s", expires.Unix(), room, participant)
	policy.ICEServers = append(policy.ICEServers, relay.ICEServer{
		URLs: []string{
			"turn:" + m.config.PublicAddress + "?transport=udp",
			"turn:" + m.config.PublicAddress + "?transport=tcp",
		},
		Username: username, Credential: m.password(username),
	})
	policy.RelayEnabled = true
	return policy
}

// remotePolicy is delivered only after both desktop endpoints have passed the
// relay's account/device checks. The credential is also tied to activeSession.id,
// so reconnecting or an administrator disconnect immediately revokes it.
func (m *conferenceTURN) remotePolicy(deviceID, sessionID, role, stunURL string, directTimeoutMS int) *relay.RTCPolicy {
	if directTimeoutMS < 500 || directTimeoutMS > 10000 {
		directTimeoutMS = 4500
	}
	policy := &relay.RTCPolicy{DirectTimeoutMS: directTimeoutMS}
	if strings.HasPrefix(stunURL, "stun:") {
		policy.ICEServers = appendUniqueICEServer(policy.ICEServers, relay.ICEServer{URLs: []string{stunURL}})
	}
	// A second destination port lets ICE detect endpoint-dependent mappings
	// without depending on a public third-party STUN service. TURN listeners
	// answer unauthenticated STUN binding requests as required by RFC 8656.
	policy.ICEServers = appendUniqueICEServer(policy.ICEServers, relay.ICEServer{URLs: []string{"stun:" + m.config.PublicAddress}})
	expires := time.Now().Add(conferenceTURNCredentialLifetime)
	username := fmt.Sprintf("%d:remote:%s:%s:%s", expires.Unix(), strings.ToLower(deviceID), strings.ToLower(sessionID), role)
	policy.ICEServers = append(policy.ICEServers, relay.ICEServer{
		URLs:     []string{"turn:" + m.config.PublicAddress + "?transport=udp"},
		Username: username, Credential: m.password(username),
	})
	policy.RelayEnabled = true
	return policy
}

func (m *conferenceTURN) authenticate(request *turn.RequestAttributes) (string, []byte, bool) {
	if request.Realm != m.config.Realm || !m.alive(request.Username) {
		return "", nil, false
	}
	return request.Username, turn.GenerateAuthKey(request.Username, m.config.Realm, m.password(request.Username)), true
}

func (m *conferenceTURN) permission(_ net.Addr, ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, network := range m.allowed {
		if network.Contains(ip) {
			return true
		}
	}
	v4 := ip.To4()
	cgnat := v4 != nil && v4[0] == 100 && (v4[1]&192) == 64
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.Equal(net.IPv4bcast) && !cgnat
}

func (m *conferenceTURN) Validate() error { return m.generator.Validate() }

func (m *conferenceTURN) AllocatePacketConn(config turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	identity, valid := m.credentialIdentity(config.UserID)
	if !valid {
		return nil, nil, errors.New("invalid conference relay identity")
	}
	user := identity.kind + ":" + identity.scope + ":" + identity.participant + ":" + identity.session
	if config.RequestedPort != 0 && (config.RequestedPort < m.config.RelayMinPort || config.RequestedPort > m.config.RelayMaxPort) {
		return nil, nil, errors.New("requested conference relay port outside configured range")
	}
	m.mu.Lock()
	state := m.users[user]
	if state == nil {
		state = &conferenceTURNUser{limiter: rate.NewLimiter(conferenceTURNBytesPerSecond, conferenceTURNBytesPerSecond)}
		m.users[user] = state
	}
	if m.closing || state.count >= conferenceTURNMaxAllocations {
		m.mu.Unlock()
		return nil, nil, errors.New("conference relay allocation quota reached")
	}
	state.count++
	m.mu.Unlock()
	conn, address, err := m.generator.AllocatePacketConn(config)
	if err != nil {
		m.mu.Lock()
		state.count--
		if state.count == 0 {
			delete(m.users, user)
		}
		m.mu.Unlock()
		return nil, nil, err
	}
	wrapped := &conferencePacketConn{PacketConn: conn, relay: m, username: config.UserID, user: user, limiter: state.limiter}
	m.mu.Lock()
	m.connections[wrapped] = true
	closing := m.closing
	m.mu.Unlock()
	if closing {
		wrapped.Close()
		return nil, nil, errors.New("conference relay is stopping")
	}
	return wrapped, address, nil
}

func (m *conferenceTURN) AllocateListener(turn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	return nil, nil, errors.New("TCP peer allocations are disabled")
}

func (m *conferenceTURN) AllocateConn(turn.AllocateConnConfig) (net.Conn, error) {
	return nil, errors.New("TCP peer allocations are disabled")
}

func (c *conferencePacketConn) Close() error {
	var err error
	c.once.Do(func() {
		m := c.relay
		m.mu.Lock()
		delete(m.connections, c)
		if state := m.users[c.user]; state != nil {
			state.count--
			if state.count == 0 {
				delete(m.users, c.user)
			}
		}
		m.mu.Unlock()
		err = c.PacketConn.Close()
	})
	return err
}

func (c *conferencePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, address, err := c.PacketConn.ReadFrom(p)
		if err != nil || c.limiter.AllowN(time.Now(), n) {
			if udp, ok := address.(*net.UDPAddr); ok && udp.IP.IsLoopback() && c.relay.hasPort(udp.Port) {
				address = &net.UDPAddr{IP: c.relay.generator.RelayAddress, Port: udp.Port}
			}
			return n, address, err
		}
	}
}

func (c *conferencePacketConn) WriteTo(p []byte, address net.Addr) (int, error) {
	if !c.limiter.AllowN(time.Now(), len(p)) {
		return len(p), nil
	}
	if udp, ok := address.(*net.UDPAddr); ok && udp.IP.Equal(c.relay.generator.RelayAddress) && c.relay.hasPort(udp.Port) {
		address = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: udp.Port}
	}
	return c.PacketConn.WriteTo(p, address)
}

func (m *conferenceTURN) hasPort(port int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for conn := range m.connections {
		if address, ok := conn.PacketConn.LocalAddr().(*net.UDPAddr); ok && address.Port == port {
			return true
		}
	}
	return false
}

func (m *conferenceTURN) snapshot() []*conferencePacketConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*conferencePacketConn, 0, len(m.connections))
	for conn := range m.connections {
		out = append(out, conn)
	}
	return out
}

func (m *conferenceTURN) prune() {
	for _, conn := range m.snapshot() {
		if !m.alive(conn.username) {
			conn.Close()
		}
	}
}

func (m *conferenceTURN) close() {
	m.once.Do(func() {
		m.mu.Lock()
		m.closing = true
		m.mu.Unlock()
		close(m.done)
		for _, conn := range m.snapshot() {
			conn.Close()
		}
		m.server.Close()
	})
}
