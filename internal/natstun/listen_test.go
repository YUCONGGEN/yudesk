package natstun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/pion/stun/v4"
)

type runningServer struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error // Written before done closes.
}

func startSTUN(t *testing.T, serve func(context.Context) error) *runningServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningServer{cancel: cancel, done: make(chan struct{})}
	go func() { r.err = serve(ctx); close(r.done) }()
	t.Cleanup(func() { cancel(); r.wait(t) })
	return r
}

func (r *runningServer) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(3 * time.Second):
		t.Fatal("STUN did not close its sockets and exit")
		return nil
	}
}

func listenTestUDP(t *testing.T, network, ip string) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP(ip)})
	if err != nil {
		if network == "udp6" {
			t.Skipf("IPv6 unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func dialTestUDP(t *testing.T, network string, addr *net.UDPAddr) *net.UDPConn {
	t.Helper()
	conn, err := net.DialUDP(network, nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func exchangeBinding(t *testing.T, conn *net.UDPConn) {
	t.Helper()
	request := pionRequest(t, true)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(request.Raw); err != nil {
		t.Fatal(err)
	}
	var buf [maxRequest]byte
	n, err := conn.Read(buf[:])
	if err != nil {
		t.Fatal(err)
	}
	m := checkResponse(t, request.Raw, buf[:n])
	var mapped stun.XORMappedAddress
	want := conn.LocalAddr().(*net.UDPAddr)
	if err := mapped.GetFrom(m); err != nil || !mapped.IP.Equal(want.IP) || mapped.Port != want.Port {
		t.Fatalf("wrong observed endpoint: %v, err=%v", mapped, err)
	}
}

func TestLargeDatagramsDoNotStopServe(t *testing.T) {
	// Exercise oversized receives wherever the sender kernel permits them.
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			ip := "127.0.0.1"
			if network == "udp6" {
				ip = "::1"
			}
			server := listenTestUDP(t, network, ip)
			r := startSTUN(t, func(ctx context.Context) error { return Serve(ctx, server) })
			client := dialTestUDP(t, network, server.LocalAddr().(*net.UDPAddr))
			for _, size := range []int{577, 2048, 8192, 65507} {
				_ = client.SetWriteDeadline(time.Now().Add(time.Second))
				if _, err := client.Write(make([]byte, size)); err != nil {
					// Darwin's default UDP send space cannot emit a 65KB datagram.
					// The smaller three oversized packets must still reach Serve.
					if size == 65507 && errors.Is(err, syscall.EMSGSIZE) {
						t.Log("kernel rejected 65KB send; smaller oversized receives covered")
						exchangeBinding(t, client)
						continue
					}
					t.Fatalf("size %d: %v", size, err)
				}
				exchangeBinding(t, client)
			}
			r.cancel()
			if err := r.wait(t); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			if _, err := server.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("socket not closed: %v", err)
			}
		})
	}
}

func TestServeCancellation(t *testing.T) {
	t.Run("before-start", func(t *testing.T) {
		server := listenTestUDP(t, "udp4", "127.0.0.1")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Serve(ctx, server); !errors.Is(err, context.Canceled) {
			t.Fatalf("result: %v", err)
		}
		if _, err := server.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("not closed: %v", err)
		}
	})
	for _, state := range []string{"idle", "traffic", "external-close"} {
		t.Run(state, func(t *testing.T) {
			server := listenTestUDP(t, "udp4", "127.0.0.1")
			r := startSTUN(t, func(ctx context.Context) error { return Serve(ctx, server) })
			client := dialTestUDP(t, "udp4", server.LocalAddr().(*net.UDPAddr))
			exchangeBinding(t, client) // Ensure cancellation occurs inside the live loop.
			if state == "traffic" {
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						select {
						case <-r.done:
							return
						default:
						}
						_ = client.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
						_, _ = client.Write(requestPacket())
					}
				}()
				t.Cleanup(func() {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("traffic worker leaked")
					}
				})
			}
			want := error(context.Canceled)
			if state == "external-close" {
				_ = server.Close()
				want = net.ErrClosed
			} else {
				r.cancel()
			}
			if err := r.wait(t); !errors.Is(err, want) {
				t.Fatalf("result=%v want=%v", err, want)
			}
		})
	}
}

func TestConcreteReplySourcesAndSharedLimit(t *testing.T) {
	ctx := context.Background()
	addresses := concreteTestAddresses(t)
	connections, err := listenAddresses(ctx, addresses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeConnections(connections) })
	limits := &responseLimiter{limiter: limiter{hosts: make(map[string]window)}}
	var runs []*runningServer
	for _, conn := range connections {
		runs = append(runs, startSTUN(t, func(ctx context.Context) error { return serve(ctx, conn, limits) }))
		// A connected UDP client accepts responses only from this exact address.
		client := dialTestUDP(t, "udp4", conn.LocalAddr().(*net.UDPAddr))
		exchangeBinding(t, client)
	}
	if connections[0].LocalAddr().(*net.UDPAddr).Port != connections[1].LocalAddr().(*net.UDPAddr).Port {
		t.Fatal("different endpoint ports")
	}
	for _, r := range runs {
		r.cancel()
		r.wait(t)
	}
	limits.mu.Lock()
	defer limits.mu.Unlock()
	// If a second boundary was crossed, only the later response remains.
	if limits.all.count < 1 || limits.all.count > 2 || len(limits.hosts) < 1 || len(limits.hosts) > limits.all.count {
		t.Fatal("listeners did not share source state")
	}
}

// macOS does not implicitly bind every 127/8 address. Use a second address
// actually assigned to this machine; never add an alias or change host routing.
func concreteTestAddresses(t *testing.T) []*net.UDPAddr {
	t.Helper()
	addresses, err := localAddresses(&net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	loop := net.ParseIP("127.0.0.1")
	for _, addr := range addresses {
		if addr.IP.To4() != nil && !addr.IP.Equal(loop) {
			return []*net.UDPAddr{{IP: loop}, {IP: addr.IP}}
		}
	}
	t.Skip("two assigned IPv4 addresses required for multi-address test")
	return nil
}

func TestListenAndServeWildcard(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0", "::1", "::"} {
		t.Run(host, func(t *testing.T) { testListenAndServe(t, host) })
	}
}

func testListenAndServe(t *testing.T, host string) {
	network, ip := "udp4", "127.0.0.1"
	if net.ParseIP(host).To4() == nil {
		network, ip = "udp6", "::1"
	}
	reservation := listenTestUDP(t, network, ip)
	port := reservation.LocalAddr().(*net.UDPAddr).Port
	_ = reservation.Close()
	r := startSTUN(t, func(ctx context.Context) error { return ListenAndServe(ctx, net.JoinHostPort(host, fmt.Sprint(port))) })
	client := dialTestUDP(t, network, &net.UDPAddr{IP: net.ParseIP(ip), Port: port})
	request := pionRequest(t, true)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_ = client.SetDeadline(time.Now().Add(100 * time.Millisecond))
		_, _ = client.Write(request.Raw)
		var buf [maxRequest]byte
		n, err := client.Read(buf[:])
		if err == nil {
			checkResponse(t, request.Raw, buf[:n])
			break
		}
		select {
		case <-r.done:
			t.Fatalf("startup: %v", r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener never became ready: %v", err)
		}
	}
	if host == "::" {
		// Match Go's default wildcard UDP behavior: IPv4 and IPv6 both work,
		// through separate concrete sockets rather than a mapped wildcard socket.
		v4 := dialTestUDP(t, "udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		exchangeBinding(t, v4)
	}
	r.cancel()
	if err := r.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	// Group cancellation released every concrete socket of this family.
	wildcard := net.IPv4zero
	if network == "udp6" {
		wildcard = net.IPv6zero
	}
	rebound, err := net.ListenUDP(network, &net.UDPAddr{IP: wildcard, Port: port})
	if err != nil {
		t.Fatalf("wildcard port leaked: %v", err)
	}
	_ = rebound.Close()
}

func TestListenAndServeCanceledOrInvalid(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ListenAndServe(ctx, "0.0.0.0:0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled listener: %v", err)
	}
	if err := ListenAndServe(context.Background(), "127.0.0.1:invalid-port"); err == nil {
		t.Fatal("invalid listening address accepted")
	}
}

func TestListenFailureClosesPartialGroup(t *testing.T) {
	addresses := concreteTestAddresses(t)
	occupied := listenTestUDP(t, "udp4", addresses[1].IP.String())
	port := occupied.LocalAddr().(*net.UDPAddr).Port
	addresses[0].Port, addresses[1].Port = port, port
	_, err := listenAddresses(context.Background(), addresses)
	if err == nil {
		t.Fatal("expected occupied port failure")
	}
	rebound, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("partial bind leaked: %v", err)
	}
	_ = rebound.Close()
}

func TestServeRejectsWildcard(t *testing.T) {
	server := listenTestUDP(t, "udp4", "0.0.0.0")
	if err := Serve(context.Background(), server); err == nil {
		t.Fatal("unsafe wildcard accepted")
	}
	if _, err := server.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("socket leaked: %v", err)
	}
}
