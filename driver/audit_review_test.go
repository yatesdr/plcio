package driver

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type auditCatalogState struct {
	delay   atomic.Bool
	slow    atomic.Bool
	churn   atomic.Bool
	change  atomic.Bool
	checks  atomic.Int32
	version atomic.Uint32
}

func auditCatalogSymbols(version uint32) []byte {
	lengths := []int{2, 3, 1}
	names := []string{"MAIN.a", "MAIN.b", "MAIN.old"}
	if version == 2 {
		lengths[0] = 4
	}
	var result []byte
	for i, name := range names {
		typ := fmt.Sprintf("ARRAY [0..%d] OF INT", lengths[i]-1)
		data := make([]byte, 33+len(name)+len(typ))
		binary.LittleEndian.PutUint32(data[:4], uint32(len(data)))
		binary.LittleEndian.PutUint32(data[4:8], 0x4040)
		binary.LittleEndian.PutUint32(data[12:16], uint32(2*lengths[i]))
		binary.LittleEndian.PutUint32(data[16:20], 2)
		binary.LittleEndian.PutUint16(data[24:26], uint16(len(name)))
		binary.LittleEndian.PutUint16(data[26:28], uint16(len(typ)))
		copy(data[30:], name)
		copy(data[31+len(name):], typ)
		result = append(result, data...)
	}
	return result
}

func auditCatalogAdapter(t *testing.T) (*ADSAdapter, *auditCatalogState) {
	t.Helper()
	state := &auditCatalogState{}
	state.version.Store(1)
	endpoint := adapterPeer(t, func(conn net.Conn) {
		for {
			req, err := adsPeerRequest(conn)
			if err != nil {
				return
			}
			var body []byte
			if binary.LittleEndian.Uint16(req[22:24]) == 1 {
				body = make([]byte, 24)
				body[4] = 3
				copy(body[8:], "Audit")
			} else {
				switch binary.LittleEndian.Uint32(req[38:42]) {
				case 0xf008:
					if state.delay.Load() {
						time.Sleep(30 * time.Millisecond)
					}
					if state.slow.Load() {
						time.Sleep(60 * time.Millisecond)
					}
					check := state.checks.Add(1)
					if state.churn.Load() {
						state.version.Add(1)
					}
					if state.change.Load() && check == 2 {
						state.version.Store(2)
					}
					data := make([]byte, 4)
					binary.LittleEndian.PutUint32(data, state.version.Load())
					body = adsPeerData(data)
				case 0xf00f:
					info := make([]byte, 24)
					binary.LittleEndian.PutUint32(info[0:4], 3)
					binary.LittleEndian.PutUint32(info[4:8], uint32(len(auditCatalogSymbols(state.version.Load()))))
					body = adsPeerData(info)
				case 0xf00b:
					body = adsPeerData(auditCatalogSymbols(state.version.Load()))
				default:
					body = []byte{0x10, 7, 0, 0}
				}
			}
			adsPeerReply(conn, req, body)
		}
	})
	a, err := NewADSAdapter(&PLCConfig{Address: endpoint, AmsNetId: "5.45.219.226.1.1", AmsPort: 851, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if _, err := a.AllTags(); err != nil {
		t.Fatal(err)
	}
	state.checks.Store(0)
	return a, state
}

func TestAuditCatalogOperationBudget(t *testing.T) {
	a, state := auditCatalogAdapter(t)
	state.delay.Store(true)
	start := time.Now()
	tags, err := a.AllTags()
	elapsed := time.Since(start)
	t.Logf("100ms AllTags budget: duration=%s, err=%v, tags=%d", elapsed, err, len(tags))
	if err == nil && elapsed > 150*time.Millisecond {
		t.Error("AllTags succeeded after restarting the operation budget for each type")
	}
	if err != nil || state.checks.Load() != 2 {
		t.Fatalf("warmed catalog used more than its two version checks: %v checks=%d", err, state.checks.Load())
	}
}

func TestAuditCatalogGenerationConsistency(t *testing.T) {
	a, state := auditCatalogAdapter(t)
	state.change.Store(true)
	tags, err := a.AllTags()
	if err != nil {
		return
	} // Refusing a changed generation is valid.
	for _, tag := range tags {
		if tag.Name == "MAIN.a" {
			if len(tag.Dimensions) != 1 || tag.TypeName != fmt.Sprintf("ARRAY [0..%d] OF INT", tag.Dimensions[0]-1) {
				t.Errorf("mixed catalog generations: %+v", tag)
			}
		}
	}
}

func TestCatalogTimeoutDiscardsProjection(t *testing.T) {
	a, state := auditCatalogAdapter(t)
	state.slow.Store(true)
	start := time.Now()
	tags, err := a.AllTags()
	if err == nil || tags != nil || time.Since(start) > 180*time.Millisecond {
		t.Fatalf("catalog exceeded original budget or exposed partial result: %v %v duration=%s", tags, err, time.Since(start))
	}
}

func TestCatalogRepeatedChangesStopAfterOneRetry(t *testing.T) {
	a, state := auditCatalogAdapter(t)
	state.churn.Store(true)
	tags, err := a.AllTags()
	if err == nil || tags != nil || state.checks.Load() > 5 {
		t.Fatalf("unbounded/inconsistent catalog refresh: tags=%v err=%v checks=%d", tags, err, state.checks.Load())
	}
}
