package secureconn

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	protocolVersion = 1
	maxHandshake    = 16 << 10
	maxRecord       = 64 << 10
)

var ErrAuthentication = errors.New("end-to-end authentication failed")

var errSequenceExhausted = errors.New("encrypted record sequence exhausted")

type clientHello struct {
	Version   int    `json:"version"`
	Ephemeral []byte `json:"ephemeral"`
	Nonce     []byte `json:"nonce"`
}

type serverHello struct {
	Ephemeral []byte `json:"ephemeral"`
	Nonce     []byte `json:"nonce"`
	Identity  []byte `json:"identity"`
	Signature []byte `json:"signature"`
	Proof     []byte `json:"proof"`
}

type clientFinish struct {
	Proof []byte `json:"proof"`
}

func DeviceID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return strings.ToUpper(hex.EncodeToString(sum[:12]))
}

// Accept authenticates the agent with its persistent Ed25519 identity and
// upgrades the connection to an end-to-end encrypted stream. The low-entropy
// pairing PIN is deliberately verified later, inside this encrypted channel,
// so the handshake cannot be used as an offline PIN oracle.
func Accept(raw net.Conn, identity ed25519.PrivateKey) (net.Conn, error) {
	if len(identity) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid agent identity key")
	}
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))
	var ch clientHello
	if err := readHandshake(raw, &ch); err != nil {
		return nil, fmt.Errorf("read client hello: %w", err)
	}
	if ch.Version != protocolVersion || len(ch.Ephemeral) != 32 || len(ch.Nonce) != 32 {
		return nil, errors.New("invalid client hello")
	}
	curve := ecdh.X25519()
	clientPublic, err := curve.NewPublicKey(ch.Ephemeral)
	if err != nil {
		return nil, err
	}
	ephemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := ephemeral.ECDH(clientPublic)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	publicIdentity := identity.Public().(ed25519.PublicKey)
	transcript := makeTranscript(ch.Ephemeral, ch.Nonce, ephemeral.PublicKey().Bytes(), nonce, publicIdentity)
	keyMaterial := deriveKeys(shared, transcript)
	sh := serverHello{
		Ephemeral: ephemeral.PublicKey().Bytes(),
		Nonce:     nonce,
		Identity:  publicIdentity,
		Signature: ed25519.Sign(identity, transcript),
		Proof:     proof(keyMaterial[:32], transcript, "agent"),
	}
	if err := writeHandshake(raw, sh); err != nil {
		return nil, err
	}
	var finish clientFinish
	if err := readHandshake(raw, &finish); err != nil {
		return nil, fmt.Errorf("read client finish: %w", err)
	}
	want := proof(keyMaterial[:32], transcript, "viewer")
	if subtle.ConstantTimeCompare(finish.Proof, want) != 1 {
		return nil, ErrAuthentication
	}
	_ = raw.SetDeadline(time.Time{})
	return newConn(raw, keyMaterial[64:96], keyMaterial[32:64])
}

// Connect verifies the expected device identity and upgrades the connection to
// an end-to-end encrypted stream. Application authentication happens within it.
func Connect(raw net.Conn, expectedDeviceID string) (net.Conn, error) {
	curve := ecdh.X25519()
	ephemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ch := clientHello{Version: protocolVersion, Ephemeral: ephemeral.PublicKey().Bytes(), Nonce: nonce}
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))
	if err := writeHandshake(raw, ch); err != nil {
		return nil, err
	}
	var sh serverHello
	if err := readHandshake(raw, &sh); err != nil {
		return nil, fmt.Errorf("read agent hello: %w", err)
	}
	if len(sh.Ephemeral) != 32 || len(sh.Nonce) != 32 || len(sh.Identity) != ed25519.PublicKeySize {
		return nil, errors.New("invalid agent hello")
	}
	if !strings.EqualFold(DeviceID(ed25519.PublicKey(sh.Identity)), expectedDeviceID) {
		return nil, errors.New("agent identity does not match device ID")
	}
	transcript := makeTranscript(ch.Ephemeral, ch.Nonce, sh.Ephemeral, sh.Nonce, sh.Identity)
	if !ed25519.Verify(ed25519.PublicKey(sh.Identity), transcript, sh.Signature) {
		return nil, errors.New("invalid agent signature")
	}
	agentEphemeral, err := curve.NewPublicKey(sh.Ephemeral)
	if err != nil {
		return nil, err
	}
	shared, err := ephemeral.ECDH(agentEphemeral)
	if err != nil {
		return nil, err
	}
	keyMaterial := deriveKeys(shared, transcript)
	want := proof(keyMaterial[:32], transcript, "agent")
	if subtle.ConstantTimeCompare(sh.Proof, want) != 1 {
		fakeProof := make([]byte, 32)
		_, _ = rand.Read(fakeProof)
		_ = writeHandshake(raw, clientFinish{Proof: fakeProof})
		return nil, ErrAuthentication
	}
	if err := writeHandshake(raw, clientFinish{Proof: proof(keyMaterial[:32], transcript, "viewer")}); err != nil {
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return newConn(raw, keyMaterial[32:64], keyMaterial[64:96])
}

func makeTranscript(parts ...[]byte) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte("yudesk-e2e-v1"))
	for _, part := range parts {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(part)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

func deriveKeys(shared, transcript []byte) []byte {
	salt := sha256.Sum256(append([]byte("yudesk-key-exchange-v1:"), transcript...))
	extract := hmac.New(sha256.New, salt[:])
	_, _ = extract.Write(shared)
	prk := extract.Sum(nil)
	return hkdfExpand(prk, append([]byte("yudesk-session-v1:"), transcript...), 96)
}

func hkdfExpand(key, info []byte, length int) []byte {
	result := make([]byte, 0, length)
	var previous []byte
	for counter := byte(1); len(result) < length; counter++ {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(previous)
		_, _ = mac.Write(info)
		_, _ = mac.Write([]byte{counter})
		previous = mac.Sum(nil)
		result = append(result, previous...)
	}
	return result[:length]
}

func proof(key, transcript []byte, role string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(transcript)
	_, _ = mac.Write([]byte(role))
	return mac.Sum(nil)
}

func writeHandshake(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) > maxHandshake {
		return errors.New("handshake message is too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writeFull(w, header[:]); err != nil {
		return err
	}
	_, err = writeFull(w, payload)
	return err
}

func readHandshake(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxHandshake {
		return errors.New("invalid handshake message size")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, value)
}

type Conn struct {
	net.Conn
	readAEAD  cipher.AEAD
	writeAEAD cipher.AEAD
	readSeq   uint64
	writeSeq  uint64
	readBuf   bytes.Reader
	readMu    sync.Mutex
	writeMu   sync.Mutex

	// Buffers belong to this connection and are protected by the corresponding
	// direction's mutex, including during underlying I/O. Never pool plaintext.
	// NewGCM uses 12-byte nonces; the trailing eight bytes are also the v1 AAD.
	readNonce   [12]byte
	writeNonce  [12]byte
	readHeader  [4]byte
	readPacket  []byte
	writePacket []byte
	readErr     error
	writeErr    error
}

func newConn(raw net.Conn, readKey, writeKey []byte) (*Conn, error) {
	readBlock, err := aes.NewCipher(readKey)
	if err != nil {
		return nil, err
	}
	writeBlock, err := aes.NewCipher(writeKey)
	if err != nil {
		return nil, err
	}
	readAEAD, err := cipher.NewGCM(readBlock)
	if err != nil {
		return nil, err
	}
	writeAEAD, err := cipher.NewGCM(writeBlock)
	if err != nil {
		return nil, err
	}
	return &Conn{Conn: raw, readAEAD: readAEAD, writeAEAD: writeAEAD}, nil
}

func (c *Conn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for c.readBuf.Len() == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		if n, err := io.ReadFull(c.Conn, c.readHeader[:]); err != nil {
			// A failure before consuming any header bytes leaves framing intact,
			// so callers can still reset a deadline and retry that read.
			if n > 0 {
				c.readErr = err
			}
			return 0, err
		}
		size := binary.BigEndian.Uint32(c.readHeader[:])
		if size < uint32(c.readAEAD.Overhead()) || size > maxRecord+uint32(c.readAEAD.Overhead()) {
			c.readErr = errors.New("invalid encrypted record size")
			return 0, c.readErr
		}
		if cap(c.readPacket) < int(size) {
			c.readPacket = make([]byte, size)
		}
		c.readPacket = c.readPacket[:size]
		if _, err := io.ReadFull(c.Conn, c.readPacket); err != nil {
			clear(c.readPacket)
			c.readErr = err
			return 0, err
		}
		nonce, aad := recordNonce(c.readSeq, c.readNonce[:])
		plaintext, err := c.readAEAD.Open(c.readPacket[:0], nonce, c.readPacket, aad)
		if err != nil {
			clear(c.readPacket)
			c.readErr = errors.New("encrypted record authentication failed")
			return 0, c.readErr
		}
		if c.readSeq == ^uint64(0) {
			c.readErr = errSequenceExhausted
		} else {
			c.readSeq++
		}
		c.readBuf.Reset(plaintext)
	}
	n, err := c.readBuf.Read(p)
	if c.readBuf.Len() == 0 {
		clear(c.readPacket)
		c.readBuf.Reset(nil)
	}
	return n, err
}

func (c *Conn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		if c.writeErr != nil {
			return written, c.writeErr
		}
		size := len(p)
		if size > maxRecord {
			size = maxRecord
		}
		nonce, aad := recordNonce(c.writeSeq, c.writeNonce[:])
		packetSize := 4 + size + c.writeAEAD.Overhead()
		if cap(c.writePacket) < packetSize {
			c.writePacket = make([]byte, packetSize)
		}
		binary.BigEndian.PutUint32(c.writePacket[:4], uint32(size+c.writeAEAD.Overhead()))
		c.writePacket = c.writeAEAD.Seal(c.writePacket[:4], nonce, p[:size], aad)
		n, err := writeFull(c.Conn, c.writePacket)
		if n == len(c.writePacket) {
			// Even a full write may return an error. Count its plaintext, but
			// never report plaintext from an incomplete authenticated record.
			written += size
			if c.writeSeq == ^uint64(0) {
				c.writeErr = errSequenceExhausted
			} else {
				c.writeSeq++
			}
		}
		if err != nil {
			// A partially sent record cannot be restarted on this stream, and
			// sealing another payload at the same sequence would reuse a nonce.
			c.writeErr = err
			return written, err
		}
		p = p[size:]
	}
	return written, nil
}

func recordNonce(sequence uint64, nonce []byte) ([]byte, []byte) {
	aad := nonce[len(nonce)-8:]
	binary.BigEndian.PutUint64(aad, sequence)
	return nonce, aad
}

func writeFull(w io.Writer, payload []byte) (int, error) {
	written := 0
	for len(payload) > 0 {
		n, err := w.Write(payload)
		if n < 0 || n > len(payload) {
			return written, io.ErrShortWrite
		}
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return written, nil
}
