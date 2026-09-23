package driver

import (
	"bytes"
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

// startPLC5Peer is a loopback PLC-5 on 127.0.0.1:44818 that answers PCCC
// Typed Read (0F/68) of integer files with word i = i+1 in element i, and
// Read-Modify-Write (0F/26) with success. It records every PCCC command.
func startPLC5Peer(t *testing.T) func() [][]byte {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:44818")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var cmds [][]byte
	var wg sync.WaitGroup
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				for {
					frame, err := eip.ReadFrame(conn)
					if err != nil {
						return
					}
					var reply *eip.Frame
					switch frame.Command {
					case 0x65:
						reply = frame.Reply(0, []byte{1, 0, 0, 0})
						reply.SessionHandle = 7
					case 0x6f:
						cpf, _ := eip.ParseRRData(frame.Data)
						packet, err := eip.ParseEipCommonPacket(cpf)
						if err != nil || len(packet.Items) != 2 {
							return
						}
						req := packet.Items[1].Data
						start := 2 + int(req[1])*2
						requester := req[start : start+7]
						cmd := append([]byte(nil), req[start+7:]...)
						mu.Lock()
						cmds = append(cmds, cmd)
						mu.Unlock()
						resp := append([]byte{0xcb, 0, 0, 0}, requester...)
						resp = append(resp, cmd[0]|0x40, 0, cmd[2], cmd[3])
						switch cmd[4] {
						case 0x68:
							// [68][off:2][total:2][06 file elem][size:2]
							elem := int(cmd[11])
							count := int(binary.LittleEndian.Uint16(cmd[12:]))
							resp = append(resp, 0x99, 0x09, byte(1+2*count), 0x42)
							for i := 0; i < count; i++ {
								resp = binary.LittleEndian.AppendUint16(resp, uint16(elem+i+1))
							}
						case 0x26:
						default:
							resp[len(resp)-3] = 0x10 // STS: illegal command
						}
						data := []byte{0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0xb2, 0, 0, 0}
						binary.LittleEndian.PutUint16(data[14:16], uint16(len(resp)))
						reply = frame.Reply(0, append(data, resp...))
					default:
						return
					}
					if _, err := conn.Write(reply.Bytes()); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), cmds...)
	}
}

// A PLC-5 adapter batches contiguous N words into one Typed Read, parses I/O
// addresses as octal, and writes bits with Read-Modify-Write.
func TestPCCCAdapterPLC5TypedReadsAndOctalIO(t *testing.T) {
	log := startPLC5Peer(t)
	a, _ := NewPCCCAdapter(&PLCConfig{Address: "127.0.0.1", Family: FamilyPLC5, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	vals, err := a.Read([]TagRequest{{Name: "N7:0"}, {Name: "N7:1"}, {Name: "N7:2"}, {Name: "I:010"}, {Name: "I:018"}})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{1, 2, 3, 9} {
		if vals[i].Error != nil || vals[i].Value != want {
			t.Errorf("%s = %#v (%v), want %d", vals[i].Name, vals[i].Value, vals[i].Error, want)
		}
	}
	if vals[4].Error == nil {
		t.Errorf("I:018 (not octal) accepted on PLC-5: %#v", vals[4].Value)
	}
	cmds := log()
	want := [][]byte{
		{0x68, 0, 0, 3, 0, 0x06, 7, 0, 3, 0}, // N7:0..2 in one typed read
		{0x68, 0, 0, 1, 0, 0x06, 1, 8, 1, 0}, // I:010 = input file 1, word 8
	}
	if len(cmds) != len(want) {
		t.Fatalf("%d requests % X", len(cmds), cmds)
	}
	for i := range want {
		if cmds[i][0] != 0x0F || !bytes.Equal(cmds[i][4:], want[i]) {
			t.Errorf("request %d = % X, want 0F 00 <tns> % X", i, cmds[i], want[i])
		}
	}

	if err := a.Write("O:010/17", true); err != nil {
		t.Fatal(err)
	}
	cmds = log()
	if got := cmds[len(cmds)-1][4:]; !bytes.Equal(got, []byte{0x26, 0x06, 0, 8, 0xFF, 0xFF, 0x00, 0x80}) {
		t.Fatalf("bit write % X", got)
	}
	if err := a.Write("L9:0", 1); err == nil {
		t.Fatal("L file write accepted on PLC-5")
	}
}

// Batches for PLC-5 stay within the typed read data limit (240 bytes minus
// the type/data parameter): 117 words per request, not 118.
func TestPCCCAdapterPLC5BatchLimit(t *testing.T) {
	log := startPLC5Peer(t)
	a, _ := NewPCCCAdapter(&PLCConfig{Address: "127.0.0.1", Family: FamilyPLC5, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	reqs := make([]TagRequest, 120)
	for i := range reqs {
		reqs[i] = TagRequest{Name: "N7:" + strconv.Itoa(i)}
	}
	vals, err := a.Read(reqs)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if v.Error != nil || v.Value != int64(i+1) {
			t.Fatalf("%s = %#v (%v)", v.Name, v.Value, v.Error)
		}
	}
	cmds := log()
	if len(cmds) != 2 || binary.LittleEndian.Uint16(cmds[0][7:]) != 117 {
		t.Fatalf("requests: % X", cmds)
	}
}
