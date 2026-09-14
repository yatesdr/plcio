package ads

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
)

// A cached-schema primitive peer, independent of client serializers. This same
// benchmark can be copied into the baseline revision for a comparable wire path.
func BenchmarkCachedPrimitiveRead(b *testing.B) {
	benchmarkPrimitiveRead(b, false, false)
}

func BenchmarkVersionValidatedPrimitiveRead(b *testing.B) {
	benchmarkPrimitiveRead(b, true, false)
}

func BenchmarkCachedTCPPrimitiveRead(b *testing.B) {
	benchmarkPrimitiveRead(b, false, true)
}

func benchmarkPrimitiveRead(b *testing.B, version, tcp bool) {
	for _, count := range []int{1, 100, 500} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			local, peer := net.Pipe()
			if tcp {
				local.Close()
				peer.Close()
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { listener.Close() })
				local, err = net.DialTimeout("tcp", listener.Addr().String(), defaultOptions().timeout)
				if err != nil {
					b.Fatal(err)
				}
				peer, err = listener.Accept()
				if err != nil {
					local.Close()
					b.Fatal(err)
				}
			}
			c := &Client{conn: newAdsConnection(local, AmsNetId{127, 0, 0, 1, 1, 1}, 32900), targetNetId: AmsNetId{5, 45, 219, 226, 1, 1}, targetPort: 851, connected: true, symbols: make(map[string]*SymbolEntry)}
			c.versionCapability, c.catalogUnavailable = 2, true
			if version {
				c.versionCapability = 1
			}
			names := make([]string, count)
			for i := range count {
				names[i] = fmt.Sprintf("MAIN.n%d", i)
				c.symbols[names[i]] = &SymbolEntry{Info: TagInfo{Name: names[i], TypeCode: TypeInt32, TypeName: "DINT", Size: 4, IndexGroup: 0x4040, IndexOffset: uint32(i * 4)}, Handle: uint32(i + 1)}
			}
			var exchanges atomic.Uint64
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					header := make([]byte, 6)
					if _, err := io.ReadFull(peer, header); err != nil {
						return
					}
					size := binary.LittleEndian.Uint32(header[2:6])
					if size > 1<<20 || size < 32 {
						return
					}
					request := make([]byte, 6+int(size))
					copy(request, header)
					if _, err := io.ReadFull(peer, request[6:]); err != nil {
						return
					}
					exchanges.Add(1)
					command := binary.LittleEndian.Uint16(request[22:24])
					var body []byte
					if binary.LittleEndian.Uint32(request[38:42]) == 0xf008 {
						body = testReadReply([]byte{0, 0, 0, 0})
					} else if command == 3 {
						body = make([]byte, 4)
					} else {
						readSize := binary.LittleEndian.Uint32(request[46:50])
						body = make([]byte, 8+int(readSize))
						binary.LittleEndian.PutUint32(body[4:8], readSize)
						if command == 2 {
							body[8] = 25
						} else {
							n := int(binary.LittleEndian.Uint32(request[42:46]))
							for i := range n {
								body[8+4*n+4*i] = 25
							}
						}
					}
					response := make([]byte, 38+len(body))
					binary.LittleEndian.PutUint32(response[2:6], uint32(32+len(body)))
					copy(response[6:14], request[14:22])
					copy(response[14:22], request[6:14])
					copy(response[22:24], request[22:24])
					response[24] = 5
					binary.LittleEndian.PutUint32(response[26:30], uint32(len(body)))
					copy(response[34:38], request[34:38])
					copy(response[38:], body)
					if _, err := peer.Write(response); err != nil {
						return
					}
				}
			}()
			b.Cleanup(func() { local.Close(); peer.Close(); <-done })
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				values, err := c.Read(names...)
				if err != nil || len(values) != count || values[count-1].Error != nil {
					b.Fatalf("cached read: %v %v", values, err)
				}
			}
			b.ReportMetric(float64(exchanges.Load())/float64(b.N), "exchanges/op")
		})
	}
}
