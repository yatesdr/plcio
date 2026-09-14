package ads_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yatesdr/plcio/ads"
	"github.com/yatesdr/plcio/driver"
	"github.com/yatesdr/plcio/metadata"
)

// The owner's full test-project matrix. These expectations describe this PLC
// project only; this opt-in test never writes values or regenerates captures.
func TestBeckhoffLiveTagMatrix(t *testing.T) {
	if os.Getenv("PLCIO_ADS_LIVE") != "1" {
		t.Skip("set PLCIO_ADS_LIVE=1 with explicit target configuration")
	}
	host, id := os.Getenv("PLCIO_ADS_HOST"), os.Getenv("PLCIO_ADS_NET_ID")
	if host == "" || id == "" {
		t.Fatal("PLCIO_ADS_HOST and PLCIO_ADS_NET_ID are required")
	}
	a, err := driver.NewADSAdapter(&driver.PLCConfig{Address: host, AmsNetId: id, AmsPort: 851, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	words := []string{"one", "two", "three", "four", "five"}
	record := map[string]any{"my_byte": uint64(15), "my_dint": int64(25), "my_sint": int64(5), "my_string": "Test structure string", "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}}
	packed := map[string]any{"my_bit1": false, "my_bit2": true, "my_bit3": false, "my_bit4": true}
	tests := []struct {
		name, declared string
		value          any
		dimensions     []metadata.Dimension
		unit, epoch    string
	}{
		{"GVL.readme", "DINT", int64(55), nil, "", ""},
		{"MAIN.test_bool", "BOOL", false, nil, "", ""},
		{"MAIN.test_sint", "SINT", int64(1), nil, "", ""},
		{"MAIN.test_usint", "USINT", uint64(2), nil, "", ""},
		{"MAIN.test_byte", "BYTE", uint64(3), nil, "", ""},
		{"MAIN.test_int", "INT", int64(4), nil, "", ""},
		{"MAIN.test_uint", "UINT", uint64(5), nil, "", ""},
		{"MAIN.test_word", "WORD", uint64(6), nil, "", ""},
		{"MAIN.test_dint", "DINT", int64(7), nil, "", ""},
		{"MAIN.test_udint", "UDINT", uint64(8), nil, "", ""},
		{"MAIN.test_dword", "DWORD", uint64(9), nil, "", ""},
		{"MAIN.test_lint", "LINT", int64(10), nil, "", ""},
		{"MAIN.test_ulint", "ULINT", uint64(11), nil, "", ""},
		{"MAIN.test_lword", "LWORD", uint64(12), nil, "", ""},
		{"MAIN.test_real", "REAL", float64(13), nil, "", ""},
		{"MAIN.test_lreal", "LREAL", float64(14), nil, "", ""},
		{"MAIN.test_string", "STRING(32)", "This is a test string.", nil, "", ""},
		{"MAIN.test_string_too_long", "STRING(80)", strings.Repeat("0123456789", 8), nil, "", ""},
		{"MAIN.test_wstring", "WSTRING(80)", "This is a test wstring.", nil, "", ""},
		{"MAIN.test_date", "DATE", uint64(1739664000), nil, metadata.UnitSeconds, metadata.EpochUnix},
		{"MAIN.test_dt", "DATE_AND_TIME", uint64(1739707932), nil, metadata.UnitSeconds, metadata.EpochUnix},
		{"MAIN.test_tod", "TIME_OF_DAY", uint64(40271000), nil, metadata.UnitMilliseconds, ""},
		{"MAIN.test_time", "TIME", uint64(95440500), nil, metadata.UnitMilliseconds, ""},
		{"MAIN.test_ltime", "LTIME", uint64(8649040500600700), nil, metadata.UnitNanoseconds, ""},
		{"MAIN.test_dint_array", "ARRAY [0..9] OF DINT", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, []metadata.Dimension{{LowerBound: 0, Length: 10}}, "", ""},
		{"MAIN.test_byte_array", "ARRAY [0..7] OF BYTE", []uint64{1, 2, 3, 4, 5, 6, 7, 8}, []metadata.Dimension{{LowerBound: 0, Length: 8}}, "", ""},
		{"MAIN.test_string_array", "ARRAY [0..4] OF STRING(80)", words, []metadata.Dimension{{LowerBound: 0, Length: 5}}, "", ""},
		{"MAIN.test_wstring_array", "ARRAY [0..4] OF WSTRING(80)", words, []metadata.Dimension{{LowerBound: 0, Length: 5}}, "", ""},
		{"MAIN.test_2d_dint_array_style1", "ARRAY [1..2] OF ARRAY [1..3] OF INT", []int64{1, 2, 3, 4, 5, 6}, []metadata.Dimension{{LowerBound: 1, Length: 2}, {LowerBound: 1, Length: 3}}, "", ""},
		{"MAIN.test_2d_dint_array_style2", "ARRAY [1..2,1..3] OF INT", []int64{1, 2, 3, 4, 5, 6}, []metadata.Dimension{{LowerBound: 1, Length: 2}, {LowerBound: 1, Length: 3}}, "", ""},
		{"MAIN.test_struct", "TEST_STRUCT", record, nil, "", ""},
		{"MAIN.test_bitpacked_struct", "PACKED_STRUCT", packed, nil, "", ""},
		{"TwinCAT_SystemInfoVarList._TaskOid_PlcTask", "OTCID", uint64(33620016), nil, "", ""},
		{"TwinCAT_SystemInfoVarList._TaskPouOid_PlcTask", "OTCID", uint64(139468801), nil, "", ""},
		{"MAIN.test_struct.my_byte", "BYTE", uint64(15), nil, "", ""},
		{"MAIN.test_struct.my_dint", "DINT", int64(25), nil, "", ""},
		{"MAIN.test_struct.my_sint", "SINT", int64(5), nil, "", ""},
		{"MAIN.test_struct.my_string", "STRING(80)", "Test structure string", nil, "", ""},
		{"MAIN.test_struct.my_dint_array", "ARRAY [1..8] OF DINT", record["my_dint_array"], []metadata.Dimension{{LowerBound: 1, Length: 8}}, "", ""},
		// TwinCAT publishes packed BOOL storage as BIT in direct lookup metadata.
		{"MAIN.test_bitpacked_struct.my_bit1", "BIT", false, nil, "", ""},
		{"MAIN.test_bitpacked_struct.my_bit2", "BIT", true, nil, "", ""},
		{"MAIN.test_bitpacked_struct.my_bit3", "BIT", false, nil, "", ""},
		{"MAIN.test_bitpacked_struct.my_bit4", "BIT", true, nil, "", ""},
	}
	tags, err := a.AllTags()
	if err != nil {
		t.Fatal(err)
	}
	catalog := make(map[string]driver.TagInfo, len(tags))
	for _, tag := range tags {
		catalog[tag.Name] = tag
	}
	requests := make([]driver.TagRequest, len(tests))
	for i, test := range tests {
		requests[i] = driver.TagRequest{Name: test.name}
	}
	values, err := a.Read(requests)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != len(tests) {
		t.Fatalf("read results %d, want %d", len(values), len(tests))
	}
	// Ignore only formatting spaces in PLC declarations, not names or axes.
	normalize := func(s string) string { return strings.Join(strings.Fields(s), "") }
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := values[i]
			if value == nil || value.Error != nil {
				t.Fatalf("read failed: %+v", value)
			}
			if !reflect.DeepEqual(value.Value, test.value) || !reflect.DeepEqual(value.StableValue, test.value) {
				t.Errorf("decoded %T %#v, want %T %#v", value.Value, value.Value, test.value, test.value)
			}
			count := 1
			var axes []uint32
			for _, dimension := range test.dimensions {
				count *= int(dimension.Length)
				axes = append(axes, dimension.Length)
			}
			if value.Count != count {
				t.Errorf("Count=%d, want %d", value.Count, count)
			}
			description, err := a.Describe(driver.TagRequest{Name: test.name})
			if err != nil {
				t.Fatal(err)
			}
			if description.Type.UnsupportedReason != "" || normalize(description.Type.DeclaredName) != normalize(test.declared) {
				t.Errorf("declaration %q unsupported=%q, want %q", description.Type.DeclaredName, description.Type.UnsupportedReason, test.declared)
			}
			if !reflect.DeepEqual(description.Type.Dimensions, test.dimensions) {
				t.Errorf("dimensions %v, want %v", description.Type.Dimensions, test.dimensions)
			}
			if description.Type.Unit != test.unit || description.Type.Epoch != test.epoch {
				t.Errorf("unit/epoch %q/%q, want %q/%q", description.Type.Unit, description.Type.Epoch, test.unit, test.epoch)
			}
			if i < 34 {
				tag, ok := catalog[test.name]
				if !ok || normalize(tag.TypeName) != normalize(test.declared) || !reflect.DeepEqual(tag.Dimensions, axes) {
					t.Errorf("catalog entry %+v present=%v, want declaration %q axes=%v", tag, ok, test.declared, axes)
				}
			}
			if expected, ok := test.value.(map[string]any); ok {
				if len(description.Type.Members) != len(expected) {
					t.Errorf("member map size %d, want %d", len(description.Type.Members), len(expected))
				}
				for _, member := range description.Type.Members {
					if _, ok := expected[member.Name]; !ok {
						t.Errorf("unexpected member %s", member.Name)
					}
				}
			}
			if test.name == "MAIN.test_struct" && len(value.Bytes) != 124 {
				t.Errorf("record bytes=%d, want complete 124-byte storage", len(value.Bytes))
			}
			t.Logf("%s: declared=%s value=%T %#v nativeType=0x%04x Count=%d bounds=%v unit=%s", test.name, description.Type.DeclaredName, value.Value, value.Value, value.DataType, value.Count, description.Type.Dimensions, description.Type.Unit)
		})
	}
	after, err := a.AllTags()
	if err != nil || !reflect.DeepEqual(tags, after) {
		t.Fatalf("member lookups changed advertised catalog: %v", err)
	}
	t.Logf("checked %d variables and %d member paths; catalog contains %d advertised entries", 34, len(tests)-34, len(tags))
}

// Opt-in read-only integration. No variable writes, routes, runtime control or
// deployment. Tests own a separate connection and release only their handles.
func TestBeckhoffLiveReadOnly(t *testing.T) {
	if os.Getenv("PLCIO_ADS_LIVE") != "1" {
		t.Skip("set PLCIO_ADS_LIVE=1 with explicit target configuration")
	}
	host, id := os.Getenv("PLCIO_ADS_HOST"), os.Getenv("PLCIO_ADS_NET_ID")
	if host == "" || id == "" {
		t.Fatal("PLCIO_ADS_HOST and PLCIO_ADS_NET_ID are required")
	}
	adapter, err := driver.NewADSAdapterWithOptions(&driver.PLCConfig{Address: host, AmsNetId: id, AmsPort: 851, Timeout: 5 * time.Second}, ads.WithMaxPayload(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Connect(); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	info, err := adapter.GetDeviceInfo()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("identity: %+v", info)
	tags, err := adapter.AllTags()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("advertised symbols: %d", len(tags))
	if len(tags) != 40 {
		t.Fatalf("catalog count %d, want 40", len(tags))
	}
	for _, tag := range tags {
		if tag.Name == "MAIN.test_2d_dint_array_style1" || tag.Name == "MAIN.test_2d_dint_array_style2" {
			if !reflect.DeepEqual(tag.Dimensions, []uint32{2, 3}) {
				t.Fatalf("catalog axes %s: %v", tag.Name, tag.Dimensions)
			}
		}
	}
	expected := map[string]any{"my_byte": uint64(15), "my_dint": int64(25), "my_sint": int64(5), "my_string": "Test structure string", "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}}
	requests := []driver.TagRequest{{Name: "MAIN.test_struct"}, {Name: "MAIN.test_bitpacked_struct"}, {Name: "MAIN.test_2d_dint_array_style1"}, {Name: "MAIN.test_2d_dint_array_style2"}, {Name: "MAIN.test_ltime"}, {Name: "MAIN.test_struct.my_dint"}}
	values, err := adapter.Read(requests)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.Error != nil {
			t.Fatalf("%s: %v", value.Name, value.Error)
		}
		t.Logf("%s %T %#v Count=%d", value.Name, value.Value, value.Value, value.Count)
	}
	for i, want := range []any{expected, map[string]any{"my_bit1": false, "my_bit2": true, "my_bit3": false, "my_bit4": true}, []int64{1, 2, 3, 4, 5, 6}, []int64{1, 2, 3, 4, 5, 6}, uint64(8649040500600700), int64(25)} {
		if !reflect.DeepEqual(values[i].Value, want) {
			t.Fatalf("live value %s differs from capture expectation: %#v", values[i].Name, values[i].Value)
		}
	}
	desc, err := adapter.Describe(driver.TagRequest{Name: "MAIN.test_struct"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("record metadata: %+v", desc)
	adapter.Client().Close()
	if adapter.IsConnected() {
		t.Fatal("closed connection reports connected")
	}
	if err := adapter.Client().Reconnect(); err != nil {
		t.Fatal(err)
	}
	again, err := adapter.Read(requests[:1])
	if err != nil || again[0].Error != nil || !reflect.DeepEqual(again[0].Value, expected) {
		t.Fatalf("reconnected record: %v %v", again, err)
	}
}
