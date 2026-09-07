package protocol

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
)

const (
	MaxHeaderSize = 1 << 20
	MaxDataSize   = 32 << 20
)

// Message metadata and binary data are framed separately so desktop frames and
// file transfers do not pay the CPU and bandwidth cost of base64 encoding.
type Message struct {
	Kind   string          `json:"kind"`
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     bool            `json:"ok,omitempty"`
	Error  string          `json:"error,omitempty"`
	Data   []byte          `json:"-"`
	Meta   map[string]any  `json:"meta,omitempty"`
}

type Conn struct {
	net.Conn
	reader *bufio.Reader
	write  chan struct{}
}

func NewConn(c net.Conn) *Conn {
	return &Conn{Conn: c, reader: bufio.NewReaderSize(c, 64<<10), write: make(chan struct{}, 1)}
}

func (c *Conn) ReadMessage() (Message, error) {
	var message Message
	var sizes [8]byte
	if _, err := io.ReadFull(c.reader, sizes[:]); err != nil {
		return message, err
	}
	headerSize := binary.BigEndian.Uint32(sizes[:4])
	dataSize := binary.BigEndian.Uint32(sizes[4:])
	if headerSize == 0 || headerSize > MaxHeaderSize || dataSize > MaxDataSize {
		return message, fmt.Errorf("invalid message sizes %d/%d", headerSize, dataSize)
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return message, err
	}
	if err := json.Unmarshal(header, &message); err != nil {
		return message, fmt.Errorf("decode message: %w", err)
	}
	if dataSize > 0 {
		message.Data = make([]byte, dataSize)
		if _, err := io.ReadFull(c.reader, message.Data); err != nil {
			return message, err
		}
	}
	return message, nil
}

func (c *Conn) WriteMessage(message Message) error {
	return c.WriteMessageContext(context.Background(), message)
}

// Cancellation while queued does not corrupt the wire or terminate a healthy
// session. Once writing has begun, cancellation closes the connection because
// a partially written encrypted record cannot safely be reused.
func (c *Conn) WriteMessageContext(ctx context.Context, message Message) error {
	data := message.Data
	message.Data = nil
	header, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(header) > MaxHeaderSize {
		return errors.New("message header is too large")
	}
	if len(data) > MaxDataSize {
		return errors.New("message data is too large")
	}
	// Keep common input/dirty-tile messages in one transport write. Each write
	// becomes its own encrypted record, so splitting prefix/header/data wastes
	// encryption work and socket calls. Large payloads are not copied again.
	capacity := 8 + len(header)
	small := capacity+len(data) <= 64<<10
	if small {
		capacity += len(data)
	}
	packet := make([]byte, 8+len(header), capacity)
	binary.BigEndian.PutUint32(packet[:4], uint32(len(header)))
	binary.BigEndian.PutUint32(packet[4:8], uint32(len(data)))
	copy(packet[8:], header)
	if small {
		packet = append(packet, data...)
	}
	select {
	case c.write <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.write }()
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Conn.Close() })
	defer stop()
	if err := writeFull(c.Conn, packet); err != nil {
		_ = c.Conn.Close()
		return err
	}
	if small {
		return nil
	}
	if err := writeFull(c.Conn, data); err != nil {
		_ = c.Conn.Close()
		return err
	}
	return nil
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func Response(id string, data []byte, meta map[string]any) Message {
	return Message{Kind: "response", ID: id, OK: true, Data: data, Meta: meta}
}
func Failure(id, message string) Message {
	return Message{Kind: "response", ID: id, OK: false, Error: message}
}
