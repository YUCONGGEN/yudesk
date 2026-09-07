package secureconn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"testing"
)

func TestHandshakeAndEncryptedStream(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentRaw, viewerRaw := net.Pipe()
	defer agentRaw.Close()
	defer viewerRaw.Close()
	type result struct {
		conn net.Conn
		err  error
	}
	agentResult := make(chan result, 1)
	go func() {
		conn, acceptErr := Accept(agentRaw, privateKey)
		agentResult <- result{conn: conn, err: acceptErr}
	}()
	viewer, err := Connect(viewerRaw, DeviceID(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	agent := <-agentResult
	if agent.err != nil {
		t.Fatal(agent.err)
	}
	want := make([]byte, 200<<10)
	if _, err := rand.Read(want); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := viewer.Write(want)
		writeDone <- writeErr
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(agent.conn, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("decrypted payload does not match")
	}
}

type recordTestConn struct {
	net.Conn
	buf    bytes.Buffer
	writes int
}

func (c *recordTestConn) Read(p []byte) (int, error)  { return c.buf.Read(p) }
func (c *recordTestConn) Write(p []byte) (int, error) { c.writes++; return c.buf.Write(p) }

func TestRecordHeaderAndCiphertextUseOneWrite(t *testing.T) {
	wire := new(recordTestConn)
	key := bytes.Repeat([]byte{17}, 32)
	writer, err := newConn(wire, key, key)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte{99}, maxRecord+13)
	if _, err := writer.Write(want); err != nil {
		t.Fatal(err)
	}
	if wire.writes != 2 {
		t.Fatalf("expected one write per record, got %d", wire.writes)
	}
	reader, err := newConn(wire, key, key)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("coalescing changed encrypted wire format")
	}
}

func TestRejectsWrongDeviceIdentity(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentRaw, viewerRaw := net.Pipe()
	defer agentRaw.Close()
	defer viewerRaw.Close()
	go func() { _, _ = Accept(agentRaw, privateKey) }()
	if _, err := Connect(viewerRaw, "BAD-DEVICE-ID"); err == nil {
		t.Fatal("wrong device identity accepted")
	}
}
