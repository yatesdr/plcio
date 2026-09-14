package ads

import (
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// This gate owns a separate connection and only reads values. Handle cleanup
// and reconnect affect that connection, not PLC variables or configuration.
func TestBeckhoffLiveReadOnlySoak(t *testing.T) {
	if os.Getenv("PLCIO_ADS_SOAK") != "1" {
		t.Skip("set PLCIO_ADS_SOAK=1 and explicit PLCIO_ADS_HOST/NET_ID for the 30-minute read-only gate")
	}
	host, id := os.Getenv("PLCIO_ADS_HOST"), os.Getenv("PLCIO_ADS_NET_ID")
	if host == "" || id == "" {
		t.Fatal("explicit target configuration is required")
	}
	c, err := Connect(host, WithAmsNetId(id), WithAmsPort(851), WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	names := []string{"MAIN.test_struct", "MAIN.test_bitpacked_struct", "MAIN.test_2d_dint_array_style1", "MAIN.test_2d_dint_array_style2", "MAIN.test_ltime", "MAIN.test_struct.my_dint"}
	want := []any{
		map[string]any{"my_byte": uint64(15), "my_dint": int64(25), "my_sint": int64(5), "my_string": "Test structure string", "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}},
		map[string]any{"my_bit1": false, "my_bit2": true, "my_bit3": false, "my_bit4": true},
		[]int64{1, 2, 3, 4, 5, 6}, []int64{1, 2, 3, 4, 5, 6}, uint64(8649040500600700), int64(25),
	}
	start := time.Now()
	t.Logf("start=%s endpoint=%s AMS=%s duration=30m interval=1s", start.UTC().Format(time.RFC3339), host, id)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var warmHandles, warmGoroutines int
	for batch := 1; batch <= 1800; batch++ {
		<-ticker.C
		values, err := c.ReadDecoded(names...)
		if err != nil {
			t.Fatalf("batch %d: %v", batch, err)
		}
		for i, value := range values {
			if value.Raw.Error != nil || !reflect.DeepEqual(value.Value, want[i]) {
				t.Fatalf("batch %d %s: error=%v value=%#v", batch, names[i], value.Raw.Error, value.Value)
			}
		}
		if batch == 30 || batch%300 == 0 {
			runtime.GC()
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			_, done, err := c.begin(false)
			if err != nil {
				t.Fatal(err)
			}
			handles, catalog, types := len(c.symbols), len(c.catalog), len(c.snapshot.entries)
			done()
			goroutines := runtime.NumGoroutine()
			if batch == 30 {
				warmHandles, warmGoroutines = handles, goroutines
			} else if handles != warmHandles || goroutines > warmGoroutines+2 {
				t.Fatalf("cache/goroutine growth: handles=%d (warm=%d), goroutines=%d (warm=%d)", handles, warmHandles, goroutines, warmGoroutines)
			}
			t.Logf("batch=%d elapsed=%s handles=%d catalog=%d types=%d goroutines=%d heap=%d totalAlloc=%d GC=%d", batch, time.Since(start).Round(time.Second), handles, catalog, types, goroutines, memory.HeapAlloc, memory.TotalAlloc, memory.NumGC)
		}
		if batch == 600 || batch == 1200 {
			c.Close()
			if c.IsConnected() {
				t.Fatal("Close leaves connected state")
			}
			if err := c.Reconnect(); err != nil {
				t.Fatal(err)
			}
			t.Logf("explicit Close/reconnect after batch %d", batch)
		}
	}
	if time.Since(start) < 30*time.Minute {
		t.Fatal("soak shorter than required duration")
	}
}
