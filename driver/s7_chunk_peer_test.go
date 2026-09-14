package driver

import (
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yatesdr/plcio/s7"
)

func TestS7DBChunkReadFixtures(t *testing.T) {
	for _, failLast := range []bool{false, true} {
		name := "complete"
		if failLast {
			name = "partial-eof"
		}
		t.Run(name, func(t *testing.T) {
			var reads atomic.Int32
			endpoint := adapterPeer(t, func(conn net.Conn) {
				for {
					req, err := tpktRead(conn)
					if err != nil {
						return
					}
					if req[1] == 0xe0 {
						tpktReply(conn, []byte{6, 0xd0, 0, 1, 0, 1, 0})
						continue
					}
					s := req[3:]
					params := s[10 : 10+int(binary.BigEndian.Uint16(s[6:]))]
					header := []byte{0x32, 3, 0, 0, s[4], s[5], 0, 2, 0, 0, 0, 0}
					var data []byte
					if params[0] == 0xf0 {
						header[7] = 8
						data = []byte{0xf0, 0, 0, 1, 0, 1, 1, 0xe0}
					} else {
						n := reads.Add(1)
						if failLast && n == 3 {
							return
						}
						if params[0] != 4 || params[1] != 1 || binary.BigEndian.Uint16(params[8:]) != 1 || params[10] != 0x84 {
							t.Errorf("DB request %x", params)
							return
						}
						size, offset := 2, 0
						if n > 1 {
							size = int(binary.BigEndian.Uint16(params[6:]))
							offset = (int(params[11])<<16 | int(params[12])<<8 | int(params[13])) / 8
							if (n == 2 && (size != 460 || offset != 0)) || (n == 3 && (size != 140 || offset != 460)) {
								t.Errorf("chunk %d size %d offset %d", n, size, offset)
							}
						}
						data = []byte{4, 1, 255, 4, byte(size * 8 >> 8), byte(size * 8)}
						for i := 0; i < size; i += 2 {
							data = append(data, 0, byte((offset+i)/2%128))
						}
						binary.BigEndian.PutUint16(header[8:], uint16(4+size))
					}
					tpktReply(conn, append([]byte{2, 0xf0, 128}, append(header, data...)...))
				}
			})
			a, _ := NewS7Adapter(&PLCConfig{Address: endpoint, Family: FamilyS7, Timeout: time.Second})
			if err := a.Connect(); err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			values, err := a.Read([]TagRequest{{Name: "DB1.600", TypeHint: "INT"}, {Name: "DB1.0[300]", TypeHint: "INT"}})
			if len(values) != 2 || values[0].Value != int64(0) || reads.Load() != 3 {
				t.Fatalf("reads %d: %v %v", reads.Load(), values, err)
			}
			if failLast {
				if !errors.Is(err, s7.ErrConnectionLost) || values[1].Error == nil || values[1].Value != nil || a.IsConnected() {
					t.Fatalf("partial %v %v", values, err)
				}
			} else {
				if err != nil || values[1].Error != nil || values[1].Count != 300 {
					t.Fatalf("complete %v %v", values, err)
				}
				array, ok := values[1].Value.([]int64)
				if !ok || len(array) != 300 {
					t.Fatalf("array %T", values[1].Value)
				}
				for i, v := range array {
					if v != int64(i%128) {
						t.Fatalf("element %d: %d", i, v)
					}
				}
			}
		})
	}
}
