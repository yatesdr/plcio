package eipadapter

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
)

// helper to build a fresh adapter on auto-assigned localhost ports
func startAdapter(t *testing.T, asm ...*Assembly) (*Adapter, context.CancelFunc) {
	t.Helper()
	cfg := Config{
		BindAddr: "127.0.0.1",
		TCPPort:  0,
		UDPPort:  0,
		IOPort:   0,
		Identity: Identity{
			VendorID:     0x1337,
			DeviceType:   0x000C,
			ProductCode:  1,
			RevMajor:     1, RevMinor: 0,
			SerialNumber: 0xC0FFEE01,
			ProductName:  "Test Adapter",
			State:        0x03,
		},
		Assemblies: asm,
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = a.Serve(ctx)
		close(done)
	}()
	cleanup := func() {
		cancel()
		<-done
	}
	// Wait for sockets to be ready (Serve binds in New, so just need start)
	time.Sleep(50 * time.Millisecond)
	return a, cleanup
}

func TestRegisterSession(t *testing.T) {
	a, stop := startAdapter(t)
	defer stop()

	tcp := a.TCPAddr()
	c := eip.NewEipClientWithPort(tcp.IP.String(), uint16(tcp.Port))
	c.SetTimeout(2 * time.Second)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Disconnect()
	if c.GetSession() == 0 {
		t.Error("session not registered")
	}
}

func TestListIdentityUDP(t *testing.T) {
	a, stop := startAdapter(t)
	defer stop()

	udp := a.udpDiscover.LocalAddr().(*net.UDPAddr)

	// Send a ListIdentity (0x63) request directly to the adapter's UDP port.
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()

	req := &eip.Frame{Command: 0x63}
	if _, err := uc.WriteToUDP(req.Bytes(), udp); err != nil {
		t.Fatal(err)
	}
	_ = uc.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := uc.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	f, err := eip.ParseFrame(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if f.Command != 0x63 || f.Status != eip.EncapStatusSuccess {
		t.Fatalf("unexpected reply cmd=0x%04X status=0x%08X", f.Command, f.Status)
	}
	// Parse the CPF and find the Identity Item (type 0x000C)
	pkt, err := eip.ParseEipCommonPacket(f.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt.Items) == 0 || pkt.Items[0].TypeId != 0x000C {
		t.Fatalf("expected Identity Item, got %+v", pkt.Items)
	}
	// Sanity: product name "Test Adapter" should appear somewhere.
	if !bytes.Contains(pkt.Items[0].Data, []byte("Test Adapter")) {
		t.Errorf("product name not found in identity payload: %x", pkt.Items[0].Data)
	}
}

func TestGetAttributeIdentityProductName(t *testing.T) {
	a, stop := startAdapter(t)
	defer stop()

	tcp := a.TCPAddr()
	c := eip.NewEipClientWithPort(tcp.IP.String(), uint16(tcp.Port))
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	// Build a Get_Attribute_Single request: path = class 0x01, inst 1, attr 7
	cipReq := make([]byte, 0, 8)
	cipReq = append(cipReq, 0x0E)             // service
	cipReq = append(cipReq, 0x03)             // path size = 3 words = 6 bytes
	cipReq = append(cipReq, 0x20, 0x01)       // class = 1
	cipReq = append(cipReq, 0x24, 0x01)       // instance = 1
	cipReq = append(cipReq, 0x30, 0x07)       // attribute = 7

	cpf := eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipReq)), Data: cipReq},
		},
	}
	resp, err := c.SendRRData(cpf)
	if err != nil {
		t.Fatalf("SendRRData: %v", err)
	}
	// Find Unconnected Data Item in reply
	var cipResp []byte
	for _, it := range resp.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			cipResp = it.Data
			break
		}
	}
	if len(cipResp) < 4 {
		t.Fatalf("short CIP response: %x", cipResp)
	}
	if cipResp[0] != 0x8E { // reply service = 0x0E | 0x80
		t.Errorf("reply service: 0x%02X want 0x8E", cipResp[0])
	}
	if cipResp[2] != cip.StatusSuccess {
		t.Errorf("status: 0x%02X want 0x00", cipResp[2])
	}
	// Data starts at byte 4: SHORT_STRING length + ASCII
	data := cipResp[4:]
	if len(data) < 1 || int(data[0]) != len("Test Adapter") {
		t.Errorf("name length wrong: %x", data)
	}
	if string(data[1:1+int(data[0])]) != "Test Adapter" {
		t.Errorf("name mismatch: %q", string(data[1:]))
	}
}

func TestAssemblyReadWrite(t *testing.T) {
	input := NewAssembly(101, AssemblyInput, 8)
	output := NewAssembly(102, AssemblyOutput, 4)
	a, stop := startAdapter(t, input, output)
	defer stop()

	// Fill the input assembly so a Get_Attribute_Single can read it.
	input.SetBytes(0, []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11})

	tcp := a.TCPAddr()
	c := eip.NewEipClientWithPort(tcp.IP.String(), uint16(tcp.Port))
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	// Read input assembly attribute 3
	cipReq := []byte{0x0E, 0x03, 0x20, 0x04, 0x24, 0x65, 0x30, 0x03}
	cpf := eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressNullId},
		{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipReq)), Data: cipReq},
	}}
	resp, err := c.SendRRData(cpf)
	if err != nil {
		t.Fatal(err)
	}
	var cipResp []byte
	for _, it := range resp.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			cipResp = it.Data
		}
	}
	if cipResp[2] != cip.StatusSuccess {
		t.Fatalf("read status: 0x%02X", cipResp[2])
	}
	got := cipResp[4:]
	want := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11}
	if !bytes.Equal(got, want) {
		t.Errorf("read data: %x vs %x", got, want)
	}

	// Write output assembly attribute 3
	writeData := []byte{0x01, 0x02, 0x03, 0x04}
	cipReq2 := []byte{0x10, 0x03, 0x20, 0x04, 0x24, 0x66, 0x30, 0x03}
	cipReq2 = append(cipReq2, writeData...)
	cpf2 := eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressNullId},
		{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipReq2)), Data: cipReq2},
	}}
	resp2, err := c.SendRRData(cpf2)
	if err != nil {
		t.Fatal(err)
	}
	var cipResp2 []byte
	for _, it := range resp2.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			cipResp2 = it.Data
		}
	}
	if cipResp2[2] != cip.StatusSuccess {
		t.Fatalf("write status: 0x%02X", cipResp2[2])
	}
	gotWritten := output.Bytes()
	if !bytes.Equal(gotWritten, writeData) {
		t.Errorf("output not written: %x vs %x", gotWritten, writeData)
	}

	// Writing to an input assembly must be rejected
	cipReq3 := []byte{0x10, 0x03, 0x20, 0x04, 0x24, 0x65, 0x30, 0x03, 0xFF, 0xFF, 0xFF, 0xFF}
	cpf3 := eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressNullId},
		{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipReq3)), Data: cipReq3},
	}}
	resp3, err := c.SendRRData(cpf3)
	if err != nil {
		t.Fatal(err)
	}
	var cipResp3 []byte
	for _, it := range resp3.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			cipResp3 = it.Data
		}
	}
	if cipResp3[2] == cip.StatusSuccess {
		t.Errorf("write to input assembly should have failed, got success")
	}
}

func TestForwardOpenAndCyclicIO(t *testing.T) {
	input := NewAssembly(101, AssemblyInput, 4)
	a, stop := startAdapter(t, input)
	defer stop()

	tcp := a.TCPAddr()
	c := eip.NewEipClientWithPort(tcp.IP.String(), uint16(tcp.Port))
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	// Build a Forward_Open request: connection path = class 4, instance 0x80
	// (no config), connection point 101 (produce only — input-only adapter).
	connPath := []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x65}
	cfg := cip.DefaultForwardOpenConfig()
	cfg.ConnectionPath = connPath
	cfg.OTConnectionSize = 0
	cfg.TOConnectionSize = uint16(input.Size + 2) // +2 for data sequence count
	foReq, _, err := cip.BuildForwardOpenRequestSmall(cfg)
	if err != nil {
		t.Fatal(err)
	}

	cpf := eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressNullId},
		{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(foReq)), Data: foReq},
	}}
	resp, err := c.SendRRData(cpf)
	if err != nil {
		t.Fatalf("Forward_Open send: %v", err)
	}
	var foResp []byte
	for _, it := range resp.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			foResp = it.Data
		}
	}
	if len(foResp) < 4 {
		t.Fatalf("short FO response: %x", foResp)
	}
	if foResp[2] != cip.StatusSuccess {
		t.Fatalf("Forward_Open status: 0x%02X, ext: %x", foResp[2], foResp[4:])
	}
	fo, err := cip.ParseForwardOpenResponse(foResp[4:])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Forward_Open accepted: O->T=0x%08X T->O=0x%08X", fo.OTConnectionID, fo.TOConnectionID)

	// Listen for cyclic I/O from the adapter on a local UDP port and send
	// one O->T packet so the adapter knows our address.
	io := a.IOAddr()
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()

	// Send a Class 0 / "wake-up" O->T packet — minimal CPF with a Sequenced
	// Address Item only, used so the adapter learns our IP:port.
	wake := buildO2TPacket(fo.OTConnectionID, 1, nil)
	if _, err := uc.WriteToUDP(wake, io); err != nil {
		t.Fatal(err)
	}

	// Update input assembly so producer has something distinctive to send.
	input.SetBytes(0, []byte{0xCA, 0xFE, 0xBA, 0xBE})

	_ = uc.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := uc.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("waiting for producer packet: %v", err)
	}

	// Parse incoming producer packet. Encap frame followed by CPF.
	f, err := eip.ParseFrame(buf[:n])
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if f.Command != eip.SendUnitData {
		t.Errorf("expected SendUnitData, got 0x%04X", f.Command)
	}
	cpfBytes, err := eip.ParseRRData(f.Data)
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := eip.ParseEipCommonPacket(cpfBytes)
	if err != nil {
		t.Fatal(err)
	}
	var dataItem *eip.EipCommonPacketItem
	for i := range pkt.Items {
		if pkt.Items[i].TypeId == 0x00B1 {
			dataItem = &pkt.Items[i]
		}
	}
	if dataItem == nil {
		t.Fatalf("no connected data item in producer packet: %+v", pkt.Items)
	}
	// Strip the 16-bit data sequence and check the assembly bytes.
	if len(dataItem.Data) < 2 {
		t.Fatal("data item too short")
	}
	body := dataItem.Data[2:]
	if !bytes.Equal(body, []byte{0xCA, 0xFE, 0xBA, 0xBE}) {
		t.Errorf("producer body: %x vs CAFEBABE", body)
	}
}

func TestUnconnectedSend(t *testing.T) {
	a, stop := startAdapter(t)
	defer stop()

	tcp := a.TCPAddr()
	c := eip.NewEipClientWithPort(tcp.IP.String(), uint16(tcp.Port))
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	// Build Unconnected_Send wrapping a Get_Attribute_Single on Identity
	embedded := []byte{0x0E, 0x03, 0x20, 0x01, 0x24, 0x01, 0x30, 0x07}
	body := make([]byte, 0)
	body = append(body, 0x0A, 0x0E) // priority/tick + timeout_ticks
	body = binary.LittleEndian.AppendUint16(body, uint16(len(embedded)))
	body = append(body, embedded...)
	// route path (to local — empty padded)
	body = append(body, 0x01, 0x00, 0x00, 0x00)

	cipReq := []byte{0x52, 0x02, 0x20, 0x06, 0x24, 0x01}
	cipReq = append(cipReq, body...)

	cpf := eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressNullId},
		{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipReq)), Data: cipReq},
	}}
	resp, err := c.SendRRData(cpf)
	if err != nil {
		t.Fatal(err)
	}
	var cipResp []byte
	for _, it := range resp.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			cipResp = it.Data
		}
	}
	if cipResp[2] != cip.StatusSuccess {
		t.Fatalf("status: 0x%02X", cipResp[2])
	}
}

// buildO2TPacket builds an O->T cyclic UDP packet for tests.
func buildO2TPacket(otConnID, netSeq uint32, data []byte) []byte {
	addr := make([]byte, 0, 12)
	addr = binary.LittleEndian.AppendUint16(addr, 0x8002)
	addr = binary.LittleEndian.AppendUint16(addr, 8)
	addr = binary.LittleEndian.AppendUint32(addr, otConnID)
	addr = binary.LittleEndian.AppendUint32(addr, netSeq)

	dItem := make([]byte, 0, 6+len(data))
	dItem = binary.LittleEndian.AppendUint16(dItem, 0x00B1)
	dItem = binary.LittleEndian.AppendUint16(dItem, uint16(2+len(data)))
	dItem = binary.LittleEndian.AppendUint16(dItem, 1) // data seq
	dItem = append(dItem, data...)

	cpf := make([]byte, 0, 2+len(addr)+len(dItem))
	cpf = binary.LittleEndian.AppendUint16(cpf, 2)
	cpf = append(cpf, addr...)
	cpf = append(cpf, dItem...)

	rrdata := eip.BuildRRData(cpf)
	f := &eip.Frame{Command: eip.SendUnitData, Data: rrdata}
	return f.Bytes()
}

// Cover the concurrent assembly access path.
func TestAssemblyConcurrent(t *testing.T) {
	a := NewAssembly(101, AssemblyInput, 16)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(seed byte) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				a.SetBytes(0, []byte{seed, seed, seed, seed})
				_ = a.Bytes()
			}
		}(byte(i))
	}
	wg.Wait()
}

// Convert a port to a string for error messages (used in case future tests
// want to print a target address).
var _ = strconv.Itoa
