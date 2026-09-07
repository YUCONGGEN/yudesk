package viewerapp

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/relay"
)

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
