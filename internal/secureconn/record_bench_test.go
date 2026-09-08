package secureconn

import (
	"bytes"
	"io"
	"net"
	"testing"
)

var recordBenchmarkSizes = []struct {
	name string
	size int
}{
	{"64B", 64},
	{"1KiB", 1 << 10},
	{"16KiB", 16 << 10},
	{"64KiB", maxRecord},
	{"256KiB", 4 * maxRecord},
}

type recordBenchmarkConn struct {
	net.Conn
	writes int64
}

func (c *recordBenchmarkConn) Write(p []byte) (int, error) {
	c.writes++
	return len(p), nil
}

// These benchmarks exclude handshakes and transport latency to measure record
// processing and steady-state allocation costs. Write also counts transport calls.
func BenchmarkRecordWrite(b *testing.B) {
	for _, size := range recordBenchmarkSizes {
		b.Run(size.name, func(b *testing.B) {
			raw := new(recordBenchmarkConn)
			key := bytes.Repeat([]byte{17}, 32)
			conn, err := newConn(raw, key, key)
			if err != nil {
				b.Fatal(err)
			}
			payload := bytes.Repeat([]byte{99}, size.size)
			if _, err := conn.Write(payload); err != nil {
				b.Fatal(err)
			}
			raw.writes = 0
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if n, err := conn.Write(payload); n != len(payload) || err != nil {
					b.Fatalf("Write = %d, %v", n, err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(raw.writes)/float64(b.N), "writes/op")
		})
	}
}

func BenchmarkRecordRead(b *testing.B) {
	for _, size := range recordBenchmarkSizes {
		b.Run(size.name, func(b *testing.B) {
			raw := new(recordTestConn)
			key := bytes.Repeat([]byte{17}, 32)
			writer, err := newConn(raw, key, key)
			if err != nil {
				b.Fatal(err)
			}
			payload := bytes.Repeat([]byte{99}, size.size)
			if _, err := writer.Write(payload); err != nil {
				b.Fatal(err)
			}
			wire := bytes.Clone(raw.buf.Bytes())
			reader, err := newConn(raw, key, key)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.ReadFull(reader, payload); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Replay a fixed authenticated stream to isolate read-side costs.
				// Production sequence numbers are never reset.
				reader.readSeq = 0
				raw.buf.Reset()
				_, _ = raw.buf.Write(wire)
				if n, err := io.ReadFull(reader, payload); n != len(payload) || err != nil {
					b.Fatalf("ReadFull = %d, %v", n, err)
				}
			}
		})
	}
}
