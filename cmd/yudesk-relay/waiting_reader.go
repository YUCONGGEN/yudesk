package main

import (
	"io"
	"net"
)

// Read waiting sockets continuously so FIN/TLS close_notify immediately removes
// stale presence. The pipe keeps at most one io.Copy buffer pending and preserves
// any early handshake bytes for Proxy after pairing; no competing socket reader.
type waitingReadConn struct {
	net.Conn
	reader *io.PipeReader
}

func (c *waitingReadConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *waitingReadConn) Close() error {
	_ = c.reader.Close()
	return c.Conn.Close()
}

func watchWaitingConnection(c net.Conn, source io.Reader) (*waitingReadConn, <-chan struct{}) {
	r, w := io.Pipe()
	done := make(chan struct{})
	go func() {
		_, err := io.Copy(w, source)
		_ = w.CloseWithError(err)
		close(done)
	}()
	return &waitingReadConn{Conn: c, reader: r}, done
}
