package protocol

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
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
	write  sync.Mutex
}

func NewConn(c net.Conn) *Conn { return &Conn{Conn: c, reader: bufio.NewReaderSize(c, 64<<10)} }

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
	var sizes [8]byte
	binary.BigEndian.PutUint32(sizes[:4], uint32(len(header)))
	binary.BigEndian.PutUint32(sizes[4:], uint32(len(data)))
	c.write.Lock()
	defer c.write.Unlock()
	if err := writeFull(c.Conn, sizes[:]); err != nil {
		return err
	}
	if err := writeFull(c.Conn, header); err != nil {
		return err
	}
	return writeFull(c.Conn, data)
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
