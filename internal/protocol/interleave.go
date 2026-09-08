package protocol

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	fragmentFlag       uint32 = 1 << 31
	fragmentSize              = 8 << 10
	fragmentHeaderSize        = 9 // flags, offset, total data length
)

type partialMessage struct {
	message  Message
	received int
}

// EnableInterleaving must only be called after the authenticated peer explicitly
// negotiates interleaveV1. Authentication itself and old peers keep the original
// wire format. It is safe to enable while the single reader waits for a packet.
func (c *Conn) EnableInterleaving() {
	c.acceptFragments.Store(true)
	c.interleave.Store(true)
}

// OfferInterleaving allows reception when advertising support in auth. Do this
// before sending the offer: the peer may stream immediately after its reply.
// Sending fragments stays disabled until its authenticated reply accepts.
func (c *Conn) OfferInterleaving() { c.acceptFragments.Store(true) }

// SetBulkRate spreads negotiated bulk fragments over the existing payload
// budget instead of bursting a whole frame into TCP's send buffer. Control
// messages are never paced. Zero preserves unlimited mode; bounds prevent an
// accidental rate from keeping a background writer asleep for hours.
func (c *Conn) SetBulkRate(bytesPerSecond int) {
	if bytesPerSecond > 0 {
		bytesPerSecond = min(1<<30, max(32<<10, bytesPerSecond))
	}
	c.bulkRate.Store(int64(max(0, bytesPerSecond)))
}

func (c *Conn) writeFragments(ctx context.Context, header, data []byte) error {
	select {
	case c.bulk <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.bulk }()
	started := false
	for offset := 0; offset < len(data); {
		rate := c.bulkRate.Load()
		if rate > 0 && time.Until(c.bulkNext) > 0 {
			timer := time.NewTimer(time.Until(c.bulkNext))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				if started {
					_ = c.Conn.Close()
				}
				return ctx.Err()
			}
		}
		n := min(fragmentSize, len(data)-offset)
		h := make([]byte, fragmentHeaderSize)
		if offset == 0 {
			h[0] |= 1
			h = append(h, header...)
		}
		if offset+n == len(data) {
			h[0] |= 2
		}
		binary.BigEndian.PutUint32(h[1:5], uint32(offset))
		binary.BigEndian.PutUint32(h[5:9], uint32(len(data)))
		sentAt := time.Now()
		if err := c.writePacket(ctx, h, data[offset:offset+n], true, false); err != nil {
			// Once any fragment is sent, queued cancellation leaves an unfinished
			// message at the peer: never reuse that session as though it completed.
			if started {
				_ = c.Conn.Close()
			}
			return err
		}
		started = true
		if rate > 0 {
			c.bulkNext = sentAt.Add(time.Duration(int64(n) * int64(time.Second) / rate))
		} else {
			c.bulkNext = time.Time{}
		}
		offset += n
	}
	return nil
}

func (c *Conn) readFragment(headerSize, dataSize uint32) (Message, bool, error) {
	bad := func() (Message, bool, error) { return Message{}, false, errors.New("invalid interleaved fragment") }
	if headerSize < fragmentHeaderSize || headerSize > MaxHeaderSize+fragmentHeaderSize || dataSize == 0 || dataSize > fragmentSize {
		return bad()
	}
	h := make([]byte, headerSize)
	if _, err := io.ReadFull(c.reader, h); err != nil {
		return Message{}, false, err
	}
	flags := h[0]
	first, last := flags&1 != 0, flags&2 != 0
	offset, total := binary.BigEndian.Uint32(h[1:5]), binary.BigEndian.Uint32(h[5:9])
	if flags&^byte(3) != 0 || total == 0 || total > MaxDataSize || offset > total || dataSize > total-offset || last != (offset+dataSize == total) {
		return bad()
	}
	if first {
		if offset != 0 || c.partial != nil || len(h) == fragmentHeaderSize {
			return bad()
		}
		var m Message
		if err := json.Unmarshal(h[fragmentHeaderSize:], &m); err != nil {
			return Message{}, false, err
		}
		m.Data = make([]byte, total)
		c.partial = &partialMessage{message: m}
	} else if len(h) != fragmentHeaderSize || c.partial == nil {
		return bad()
	}
	p := c.partial
	if uint32(p.received) != offset || uint32(len(p.message.Data)) != total {
		return bad()
	}
	if _, err := io.ReadFull(c.reader, p.message.Data[offset:offset+dataSize]); err != nil {
		return Message{}, false, err
	}
	p.received += int(dataSize)
	if !last {
		return Message{}, false, nil
	}
	c.partial = nil
	return p.message, true, nil
}
