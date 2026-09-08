package secureconn

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

type recordFuncConn struct {
	net.Conn
	read  func([]byte) (int, error)
	write func([]byte) (int, error)
}

func (c *recordFuncConn) Read(p []byte) (int, error)  { return c.read(p) }
func (c *recordFuncConn) Write(p []byte) (int, error) { return c.write(p) }

func testRecordConn(t *testing.T, raw net.Conn, key []byte) *Conn {
	t.Helper()
	c, err := newConn(raw, key, key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Implement the v1 format independently of recordNonce and Conn's buffers.
func v1RecordAEAD(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

func v1SealRecord(aead cipher.AEAD, seq uint64, plaintext []byte) []byte {
	var nonce [12]byte
	var aad [8]byte
	binary.BigEndian.PutUint64(nonce[4:], seq)
	binary.BigEndian.PutUint64(aad[:], seq)
	ciphertext := aead.Seal(nil, nonce[:], plaintext, aad[:])
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(ciphertext)))
	return append(header, ciphertext...)
}

func v1OpenRecord(t *testing.T, aead cipher.AEAD, seq uint64, wire *bytes.Reader) []byte {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(wire, header[:]); err != nil {
		t.Fatal(err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size < 16 || size > (64<<10)+16 {
		t.Fatalf("v1 record size = %d", size)
	}
	ciphertext := make([]byte, size)
	if _, err := io.ReadFull(wire, ciphertext); err != nil {
		t.Fatal(err)
	}
	var nonce [12]byte
	var aad [8]byte
	binary.BigEndian.PutUint64(nonce[4:], seq)
	binary.BigEndian.PutUint64(aad[:], seq)
	plaintext, err := aead.Open(nil, nonce[:], ciphertext, aad[:])
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func TestRecordV1Compatibility(t *testing.T) {
	if protocolVersion != 1 || maxRecord != 64<<10 {
		t.Fatal("v1 version or record limit changed")
	}
	key := bytes.Repeat([]byte{17}, 32)
	aead := v1RecordAEAD(t, key)
	raw := new(recordTestConn)
	writer := testRecordConn(t, raw, key)
	var wantWire, wantPlain []byte
	var seq uint64
	for _, size := range []int{0, 1, 64, 1024, 16 << 10, maxRecord - 1, maxRecord, maxRecord + 1, 4*maxRecord + 23} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i*31 + size)
		}
		original := bytes.Clone(payload)
		n, err := writer.Write(payload)
		if n != size || err != nil {
			t.Fatalf("Write(%d) = %d, %v", size, n, err)
		}
		if !bytes.Equal(payload, original) {
			t.Fatal("Write changed caller plaintext")
		}
		wantPlain = append(wantPlain, payload...)
		for len(payload) > 0 {
			n := min(len(payload), 64<<10)
			wantWire = append(wantWire, v1SealRecord(aead, seq, payload[:n])...)
			seq++
			payload = payload[n:]
		}
	}
	if !bytes.Equal(raw.buf.Bytes(), wantWire) {
		t.Fatal("wire bytes differ from independent v1 implementation")
	}
	if raw.writes != int(seq) || writer.writeSeq != seq {
		t.Fatalf("writes=%d, sequence=%d; want %d", raw.writes, writer.writeSeq, seq)
	}
	reader := testRecordConn(t, &recordFuncConn{read: bytes.NewReader(wantWire).Read}, key)
	got := make([]byte, len(wantPlain))
	for pos := 0; pos < len(got); {
		n, err := reader.Read(got[pos:min(pos+137, len(got))])
		if n == 0 || err != nil {
			t.Fatalf("Read at %d = %d, %v", pos, n, err)
		}
		pos += n
	}
	if !bytes.Equal(got, wantPlain) || reader.readSeq != seq {
		t.Fatal("reading independent v1 records changed plaintext or sequence")
	}
}

func TestRecordShortWrites(t *testing.T) {
	key := bytes.Repeat([]byte{23}, 32)
	aead := v1RecordAEAD(t, key)
	payload := bytes.Repeat([]byte{97}, maxRecord+29)
	want := append(v1SealRecord(aead, 0, payload[:maxRecord]), v1SealRecord(aead, 1, payload[maxRecord:])...)
	for _, chunk := range []int{1, 3, 4, 7, 1031} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			var wire bytes.Buffer
			calls := 0
			raw := &recordFuncConn{write: func(p []byte) (int, error) {
				calls++
				return wire.Write(p[:min(chunk, len(p))])
			}}
			conn := testRecordConn(t, raw, key)
			n, err := conn.Write(payload)
			if n != len(payload) || err != nil || !bytes.Equal(wire.Bytes(), want) {
				t.Fatalf("short-write stream: n=%d, err=%v, wire equal=%v", n, err, bytes.Equal(wire.Bytes(), want))
			}
			wantCalls := (maxRecord+20+chunk-1)/chunk + (29+20+chunk-1)/chunk
			if calls != wantCalls || conn.writeSeq != 2 {
				t.Fatalf("calls=%d, sequence=%d; want %d, 2", calls, conn.writeSeq, wantCalls)
			}
		})
	}
}

func TestRecordWriteFailures(t *testing.T) {
	boom := errors.New("transport write failed")
	type step struct {
		n   int // -1 means the full supplied slice; -3 means an oversized count.
		err error
	}
	cases := []struct {
		name  string
		steps []step
		wantN int
		seq   uint64
		err   error
	}{
		{"zero-error", []step{{0, boom}}, 0, 0, boom},
		{"zero-nil", []step{{0, nil}}, 0, 0, io.ErrShortWrite},
		{"partial-header", []step{{2, boom}}, 0, 0, boom},
		{"header-only", []step{{4, boom}}, 0, 0, boom},
		{"partial-ciphertext", []step{{13, boom}}, 0, 0, boom},
		{"partial-tag", []step{{maxRecord + 19, boom}}, 0, 0, boom},
		{"short-then-zero", []step{{7, nil}, {0, nil}}, 0, 0, io.ErrShortWrite},
		{"short-then-error", []step{{7, nil}, {3, boom}}, 0, 0, boom},
		{"full-with-error", []step{{-1, boom}}, maxRecord, 1, boom},
		{"short-then-full-error", []step{{7, nil}, {-1, boom}}, maxRecord, 1, boom},
		{"second-zero", []step{{-1, nil}, {0, nil}}, maxRecord, 1, io.ErrShortWrite},
		{"second-partial", []step{{-1, nil}, {9, boom}}, maxRecord, 1, boom},
		{"second-full-error", []step{{-1, nil}, {-1, boom}}, maxRecord + 23, 2, boom},
		{"negative-count", []step{{-2, nil}}, 0, 0, io.ErrShortWrite},
		{"oversized-count", []step{{-3, nil}}, 0, 0, io.ErrShortWrite},
	}
	key := bytes.Repeat([]byte{41}, 32)
	aead := v1RecordAEAD(t, key)
	payload := bytes.Repeat([]byte{101}, maxRecord+23)
	wantWire := append(v1SealRecord(aead, 0, payload[:maxRecord]), v1SealRecord(aead, 1, payload[maxRecord:])...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var wire bytes.Buffer
			raw := &recordFuncConn{write: func(p []byte) (int, error) {
				if calls == len(tc.steps) {
					t.Fatal("unexpected write after failure")
				}
				step := tc.steps[calls]
				calls++
				n := step.n
				if n == -1 {
					n = len(p)
				} else if n == -3 {
					n = len(p) + 1
				}
				if n >= 0 && n <= len(p) {
					_, _ = wire.Write(p[:n])
				}
				return n, step.err
			}}
			conn := testRecordConn(t, raw, key)
			n, err := conn.Write(payload)
			if n != tc.wantN || !errors.Is(err, tc.err) || conn.writeSeq != tc.seq {
				t.Fatalf("Write = %d, %v, seq=%d; want %d, %v, seq=%d", n, err, conn.writeSeq, tc.wantN, tc.err, tc.seq)
			}
			if !bytes.Equal(wire.Bytes(), wantWire[:wire.Len()]) {
				t.Fatal("partial wire differs from v1")
			}
			if n, err := conn.Write([]byte("different plaintext")); n != 0 || !errors.Is(err, tc.err) {
				t.Fatalf("retry = %d, %v", n, err)
			}
			if calls != len(tc.steps) {
				t.Fatalf("write calls=%d, want %d", calls, len(tc.steps))
			}
		})
	}
}

func TestRecordRejectsInvalidInput(t *testing.T) {
	key := bytes.Repeat([]byte{17}, 32)
	aead := v1RecordAEAD(t, key)
	valid := v1SealRecord(aead, 0, []byte("authenticated plaintext"))
	withSize := func(size uint32) []byte {
		p := bytes.Clone(valid)
		binary.BigEndian.PutUint32(p, size)
		return p
	}
	tamper := func(offset int) []byte {
		p := bytes.Clone(valid)
		p[offset] ^= 1
		return p
	}
	cases := []struct {
		name string
		wire []byte
		seq  uint64
	}{
		{"ciphertext", tamper(4), 0},
		{"tag", tamper(len(valid) - 1), 0},
		{"zero-size", withSize(0), 0},
		{"undersized", withSize(15), 0},
		{"oversized", withSize((64 << 10) + 17), 0},
		{"max-uint32-size", withSize(^uint32(0)), 0},
		{"changed-valid-size", withSize(16), 0},
		{"partial-header", valid[:2], 0},
		{"missing-ciphertext", valid[:4], 0},
		{"partial-ciphertext", valid[:len(valid)-1], 0},
		{"replayed-sequence", valid, 1},
		{"future-sequence", v1SealRecord(aead, 1, []byte("future")), 0},
		{"wrong-key", v1SealRecord(v1RecordAEAD(t, bytes.Repeat([]byte{18}, 32)), 0, []byte("wrong key")), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := bytes.NewReader(tc.wire)
			calls := 0
			raw := &recordFuncConn{read: func(p []byte) (int, error) {
				calls++
				return wire.Read(p)
			}}
			conn := testRecordConn(t, raw, key)
			conn.readSeq = tc.seq
			p := bytes.Repeat([]byte{0xcc}, 100)
			n, err := conn.Read(p)
			if n != 0 || err == nil || conn.readSeq != tc.seq {
				t.Fatalf("Read = %d, %v, seq=%d", n, err, conn.readSeq)
			}
			if !bytes.Equal(p, bytes.Repeat([]byte{0xcc}, len(p))) {
				t.Fatal("unauthenticated plaintext escaped")
			}
			callsBefore := calls
			if n, again := conn.Read(p); n != 0 || again != err || calls != callsBefore {
				t.Fatalf("failure was not terminal: n=%d, err=%v, calls=%d/%d", n, again, calls, callsBefore)
			}
			for _, v := range conn.readPacket {
				if v != 0 {
					t.Fatal("failed record buffer was not cleared")
				}
			}
		})
	}
}

func TestRecordReadBoundaryRetryAndEmptyRecord(t *testing.T) {
	key := bytes.Repeat([]byte{17}, 32)
	aead := v1RecordAEAD(t, key)
	wire := bytes.NewReader(append(v1SealRecord(aead, 0, nil), v1SealRecord(aead, 1, []byte("ok"))...))
	timeout := &net.OpError{Op: "read", Err: errors.New("temporary transport failure")}
	calls := 0
	conn := testRecordConn(t, &recordFuncConn{read: func(p []byte) (int, error) {
		calls++
		if calls == 1 {
			return 0, timeout
		}
		return wire.Read(p[:min(3, len(p))])
	}}, key)
	if n, err := conn.Read(nil); n != 0 || err != nil || calls != 0 {
		t.Fatalf("empty Read = %d, %v, calls=%d", n, err, calls)
	}
	p := make([]byte, 2)
	if n, err := conn.Read(p); n != 0 || err != timeout {
		t.Fatalf("boundary failure = %d, %v", n, err)
	}
	if n, err := conn.Read(p); n != 2 || err != nil || string(p) != "ok" || conn.readSeq != 2 {
		t.Fatalf("retry past empty record = %d, %v, %q, seq=%d", n, err, p, conn.readSeq)
	}
}

func TestRecordSequenceExhaustion(t *testing.T) {
	key := bytes.Repeat([]byte{17}, 32)
	raw := new(recordTestConn)
	writer := testRecordConn(t, raw, key)
	writer.writeSeq = ^uint64(0)
	payload := bytes.Repeat([]byte{99}, maxRecord+1)
	if n, err := writer.Write(payload); n != maxRecord || !errors.Is(err, errSequenceExhausted) {
		t.Fatalf("last sequence Write = %d, %v", n, err)
	}
	want := v1SealRecord(v1RecordAEAD(t, key), ^uint64(0), payload[:maxRecord])
	if raw.writes != 1 || writer.writeSeq != ^uint64(0) || !bytes.Equal(raw.buf.Bytes(), want) {
		t.Fatal("last sequence was encoded incorrectly or wrapped")
	}
	if n, err := writer.Write(payload); n != 0 || !errors.Is(err, errSequenceExhausted) || raw.writes != 1 {
		t.Fatalf("exhausted Write = %d, %v, writes=%d", n, err, raw.writes)
	}
	reader := testRecordConn(t, raw, key)
	reader.readSeq = ^uint64(0)
	got := make([]byte, maxRecord)
	if n, err := reader.Read(got[:1]); n != 1 || err != nil {
		t.Fatalf("last sequence Read = %d, %v", n, err)
	}
	if _, err := io.ReadFull(reader, got[1:]); err != nil || !bytes.Equal(got, payload[:maxRecord]) {
		t.Fatalf("draining last sequence: %v", err)
	}
	if n, err := reader.Read(got); n != 0 || !errors.Is(err, errSequenceExhausted) || reader.readSeq != ^uint64(0) {
		t.Fatalf("exhausted Read = %d, %v, seq=%d", n, err, reader.readSeq)
	}
}

func TestRecordBufferReuseAndIsolation(t *testing.T) {
	key := bytes.Repeat([]byte{17}, 32)
	aead := v1RecordAEAD(t, key)
	payload := bytes.Repeat([]byte{97}, maxRecord)
	fixture := append(v1SealRecord(aead, 0, payload), v1SealRecord(aead, 1, []byte("next"))...)
	newTestConn := func() *Conn {
		return testRecordConn(t, &recordFuncConn{
			read: bytes.NewReader(fixture).Read,
			write: func(p []byte) (int, error) {
				return len(p), nil
			},
		}, key)
	}
	first, second := newTestConn(), newTestConn()
	p := make([]byte, maxRecord)
	if n, err := first.Read(p[:1]); n != 1 || err != nil {
		t.Fatalf("first Read = %d, %v", n, err)
	}
	readBacking := &first.readPacket[0]
	if n, err := second.Read(p); n != maxRecord || err != nil {
		t.Fatalf("second Read = %d, %v", n, err)
	}
	if _, err := io.ReadFull(first, p[1:]); err != nil || !bytes.Equal(p, payload) {
		t.Fatal("another connection overwrote unread plaintext")
	}
	if _, err := io.ReadFull(first, p[:4]); err != nil || string(p[:4]) != "next" {
		t.Fatalf("next record: %v", err)
	}
	if readBacking != &first.readPacket[0] || cap(first.readPacket) > maxRecord+16 {
		t.Fatal("read buffer was not reused or exceeds the record limit")
	}
	for _, v := range first.readPacket[:cap(first.readPacket)] {
		if v != 0 {
			t.Fatal("consumed plaintext was retained")
		}
	}
	if _, err := first.Write(payload); err != nil {
		t.Fatal(err)
	}
	writeBacking := &first.writePacket[0]
	snapshot := bytes.Clone(first.writePacket)
	if _, err := second.Write([]byte("other connection")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot, first.writePacket) {
		t.Fatal("another connection overwrote the write buffer")
	}
	if _, err := first.Write([]byte("small")); err != nil {
		t.Fatal(err)
	}
	if writeBacking != &first.writePacket[0] || cap(first.writePacket) > maxRecord+20 {
		t.Fatal("write buffer was not reused or exceeds the record limit")
	}
}

func TestRecordConcurrentWrites(t *testing.T) {
	const workers, messages, size = 8, 32, 1024
	key := bytes.Repeat([]byte{17}, 32)
	var wire bytes.Buffer
	calls := 0
	conn := testRecordConn(t, &recordFuncConn{write: func(p []byte) (int, error) {
		// The supplied ciphertext must stay unchanged until Write returns.
		runtime.Gosched()
		calls++
		n, err := wire.Write(p)
		runtime.Gosched()
		return n, err
	}}, key)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			p := bytes.Repeat([]byte{byte(worker)}, size)
			for message := 0; message < messages; message++ {
				binary.BigEndian.PutUint64(p, uint64(worker*messages+message))
				if n, err := conn.Write(p); n != size || err != nil {
					t.Errorf("concurrent Write = %d, %v", n, err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if calls != workers*messages || conn.writeSeq != workers*messages {
		t.Fatalf("calls=%d, sequence=%d", calls, conn.writeSeq)
	}
	aead := v1RecordAEAD(t, key)
	reader := bytes.NewReader(wire.Bytes())
	seen := make(map[uint64]bool)
	for seq := uint64(0); seq < workers*messages; seq++ {
		p := v1OpenRecord(t, aead, seq, reader)
		if len(p) != size {
			t.Fatalf("record plaintext length=%d", len(p))
		}
		id := binary.BigEndian.Uint64(p)
		if id >= workers*messages || seen[id] || !bytes.Equal(p[8:], bytes.Repeat([]byte{byte(id / messages)}, size-8)) {
			t.Fatalf("concurrent payload corrupted or duplicated: %d", id)
		}
		seen[id] = true
	}
	if reader.Len() != 0 {
		t.Fatal("unexpected trailing wire bytes")
	}
}

func TestRecordConcurrentReads(t *testing.T) {
	const workers, messages, size = 8, 32, 64
	key := bytes.Repeat([]byte{17}, 32)
	aead := v1RecordAEAD(t, key)
	var wire []byte
	for record := 0; record < workers*messages/4; record++ {
		p := make([]byte, 4*size)
		for segment := 0; segment < 4; segment++ {
			id := record*4 + segment
			copy(p[segment*size:], bytes.Repeat([]byte{byte(id)}, size))
			binary.BigEndian.PutUint64(p[segment*size:], uint64(id))
		}
		wire = append(wire, v1SealRecord(aead, uint64(record), p)...)
	}
	reader := bytes.NewReader(wire)
	conn := testRecordConn(t, &recordFuncConn{read: func(p []byte) (int, error) {
		runtime.Gosched()
		return reader.Read(p[:min(7, len(p))])
	}}, key)
	ids := make(chan uint64, workers*messages)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := make([]byte, size)
			for message := 0; message < messages; message++ {
				if n, err := conn.Read(p); n != size || err != nil {
					t.Errorf("concurrent Read = %d, %v", n, err)
					return
				}
				id := binary.BigEndian.Uint64(p)
				if !bytes.Equal(p[8:], bytes.Repeat([]byte{byte(id)}, size-8)) {
					t.Errorf("concurrent read buffer corrupted: %d", id)
					return
				}
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[uint64]bool)
	for id := range ids {
		if id >= workers*messages || seen[id] {
			t.Fatalf("invalid or repeated read: %d", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*messages || conn.readSeq != workers*messages/4 {
		t.Fatalf("read %d messages, sequence=%d", len(seen), conn.readSeq)
	}
}

func TestRecordConcurrentFullDuplex(t *testing.T) {
	leftRaw, rightRaw := net.Pipe()
	defer leftRaw.Close()
	defer rightRaw.Close()
	_ = leftRaw.SetDeadline(time.Now().Add(10 * time.Second))
	_ = rightRaw.SetDeadline(time.Now().Add(10 * time.Second))
	leftKey, rightKey := bytes.Repeat([]byte{17}, 32), bytes.Repeat([]byte{23}, 32)
	left, err := newConn(leftRaw, leftKey, rightKey)
	if err != nil {
		t.Fatal(err)
	}
	right, err := newConn(rightRaw, rightKey, leftKey)
	if err != nil {
		t.Fatal(err)
	}
	leftPayload := bytes.Repeat([]byte("left"), maxRecord+13)
	rightPayload := bytes.Repeat([]byte("right"), maxRecord+17)
	var wg sync.WaitGroup
	for _, direction := range []struct {
		conn *Conn
		send []byte
		recv []byte
	}{{left, leftPayload, rightPayload}, {right, rightPayload, leftPayload}} {
		wg.Add(2)
		go func(conn *Conn, p []byte) {
			defer wg.Done()
			for len(p) > 0 {
				size := min(len(p), maxRecord+7)
				if n, err := conn.Write(p[:size]); n != size || err != nil {
					t.Errorf("duplex Write = %d, %v", n, err)
					return
				}
				p = p[size:]
			}
		}(direction.conn, direction.send)
		go func(conn *Conn, want []byte) {
			defer wg.Done()
			got := make([]byte, len(want))
			for pos := 0; pos < len(got); {
				n, err := conn.Read(got[pos:min(pos+997, len(got))])
				if n == 0 || err != nil {
					t.Errorf("duplex Read = %d, %v", n, err)
					return
				}
				pos += n
			}
			if !bytes.Equal(got, want) {
				t.Error("duplex plaintext mismatch")
			}
		}(direction.conn, direction.recv)
	}
	wg.Wait()
}
