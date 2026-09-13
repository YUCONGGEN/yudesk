package viewerapp

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/relay"
)

func TestWindowsManagedRendererLossKeepsSingletonAlive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native lifecycle")
	}
	h, err := newViewerHost("127.0.0.1:0", "renderer-recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.window.managed.Store(true)
	h.window.headless = false
	h.handleWindowClosed()
	select {
	case <-h.ctx.Done():
		t.Fatal("private renderer loss terminated the main process")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDefaultViewerPortFallsBackWhenOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", defaultViewerWebAddress)
	if err == nil {
		defer occupied.Close()
	}
	h, err := newViewerHost(defaultViewerWebAddress, "port-fallback")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if h.listener.Addr().String() == defaultViewerWebAddress {
		t.Fatal("viewer host reused the occupied default port")
	}
}

func TestWindowsManagedWatchDropDoesNotExitHost(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native lifecycle")
	}
	h, err := newViewerHost("127.0.0.1:0", "watch-recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.window.managed.Store(true)
	h.window.headless = false
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+h.listener.Addr().String()+"/api/ui/watch?access_token=watch-recovery", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	// Wait beyond the tracker's close grace. Losing only the loopback page
	// stream during a remote-network transition must not stop the singleton.
	time.Sleep(850 * time.Millisecond)
	select {
	case <-h.ctx.Done():
		t.Fatal("managed page/watch loss terminated the main process")
	default:
	}
}

func TestConcurrentWindowWakeIsCoalesced(t *testing.T) {
	h, err := newViewerHost("127.0.0.1:0", "wake-coalesce")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.window.headless = true
	h.window.candidates = nil
	// Seed the successful-show debounce without opening a real browser. All
	// duplicate launcher requests in this interval must return without a second
	// renderer start attempt.
	h.lastShow = time.Now()
	var failures atomic.Int32
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	done := make(chan struct{}, 12)
	for range 12 {
		go func() {
			request, _ := http.NewRequest(http.MethodPost, "http://"+h.listener.Addr().String()+"/api/ui/show?access_token=wake-coalesce", nil)
			response, requestErr := client.Do(request)
			if requestErr != nil || response.StatusCode != http.StatusOK {
				failures.Add(1)
			}
			if response != nil {
				response.Body.Close()
			}
			done <- struct{}{}
		}()
	}
	for range 12 {
		<-done
	}
	if failures.Load() != 0 {
		t.Fatalf("duplicate launch requests were not coalesced: %d failures", failures.Load())
	}
}

func TestViewerCloseDuringConnectingCancelsDial(t *testing.T) {
	h, err := newViewerHost("127.0.0.1:0", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	url := "http://" + h.listener.Addr().String() + "/api/ui/watch?access_token=test-token"
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_, _ = conn.Write([]byte("WAIT\n"))
		close(accepted)
		<-h.ctx.Done()
	}()
	done := make(chan error, 1)
	go func() {
		_, err := relay.DialWithContext(h.ctx, listener.Addr().String(), relay.DialOptions{}, relay.Hello{Role: "viewer", ID: "A"})
		done <- err
	}()
	<-accepted
	// There is deliberately no session page or listener yet.
	response.Body.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed window did not cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connecting Viewer survived browser close")
	}
}

func TestViewerPageTransitionKeepsWatchAlive(t *testing.T) {
	h, err := newViewerHost("127.0.0.1:0", "token")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	response, err := http.Get("http://" + h.listener.Addr().String() + "/api/ui/watch?access_token=token")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first, _ := listenViewerPage("", h)
	attachViewerPage(first, http.NotFoundHandler())
	_ = first.Close()
	second, _ := listenViewerPage("", h)
	attachViewerPage(second, http.NotFoundHandler())
	defer second.Close()
	if h.ctx.Err() != nil {
		t.Fatal("transition canceled process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	select {
	case <-h.ctx.Done():
		t.Fatal("watch lost on transition")
	case <-ctx.Done():
	}
}
