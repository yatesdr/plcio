package ads

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
)

// mapTypeFromName attempts to determine the type code from the type name string.
// This is used as a fallback when the ADS type code is unknown (e.g., LTIME, TOD).
func mapTypeFromName(typeName string, size uint32) uint16 {
	// Normalize to uppercase for matching
	upper := strings.ToUpper(typeName)

	// Handle array types by extracting the base type
	if strings.HasPrefix(upper, "ARRAY") {
		// Extract base type from "ARRAY [x..y] OF TYPE"
		if idx := strings.Index(upper, " OF "); idx != -1 {
			upper = strings.TrimSpace(upper[idx+4:])
		}
	}

	// Match known type names
	switch upper {
	case "LTIME":
		return TypeLTime
	case "TIME":
		return TypeTime
	case "DATE":
		return TypeDate
	case "TIME_OF_DAY", "TOD":
		return TypeTimeOfDay
	case "DATE_AND_TIME", "DT":
		return TypeDateTime
	case "BOOL":
		return TypeBool
	case "BYTE", "USINT":
		return TypeByte
	case "SINT":
		return TypeSByte
	case "WORD", "UINT":
		return TypeWord
	case "INT":
		return TypeInt16
	case "DWORD", "UDINT":
		return TypeDWord
	case "DINT":
		return TypeInt32
	case "LWORD", "ULINT":
		return TypeLWord
	case "LINT":
		return TypeInt64
	case "REAL":
		return TypeReal
	case "LREAL":
		return TypeLReal
	case "STRING":
		return TypeString
	case "WSTRING":
		return TypeWString
	}

	// Check if type name starts with STRING (e.g., "STRING(80)")
	if strings.HasPrefix(upper, "STRING") {
		return TypeString
	}
	if strings.HasPrefix(upper, "WSTRING") {
		return TypeWString
	}

	return TypeUnknown
}

// mapAdsType maps TwinCAT ADST_* type enum to our type codes.
func mapAdsType(adsType uint32) uint16 {
	switch adsType {
	case 0: // ADST_VOID
		return TypeVoid
	case 16: // ADST_INT8
		return TypeSByte
	case 17: // ADST_UINT8
		return TypeByte
	case 2: // ADST_INT16
		return TypeInt16
	case 18: // ADST_UINT16
		return TypeWord
	case 3: // ADST_INT32
		return TypeInt32
	case 19: // ADST_UINT32
		return TypeDWord
	case 20: // ADST_INT64
		return TypeInt64
	case 21: // ADST_UINT64
		return TypeLWord
	case 4: // ADST_REAL32
		return TypeReal
	case 5: // ADST_REAL64
		return TypeLReal
	case 30: // ADST_STRING
		return TypeString
	case 31: // ADST_WSTRING
		return TypeWString
	case 33: // ADST_BOOL / ADST_BIT
		return TypeBool
	default:
		// For complex types, return TypeUnknown
		return TypeUnknown
	}
}

// parseArrayCountFromTypeName extracts array element count from TwinCAT type names.
// Examples: "ARRAY [0..4] OF STRING" -> 5, "ARRAY [1..10] OF INT" -> 10
func parseArrayCountFromTypeName(typeName string) int {
	// Look for pattern like "[0..4]" or "[1..10]"
	startIdx := strings.Index(typeName, "[")
	endIdx := strings.Index(typeName, "]")
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return 1
	}

	bounds := typeName[startIdx+1 : endIdx]
	// Split by ".." to get lower and upper bounds
	parts := strings.Split(bounds, "..")
	if len(parts) != 2 {
		return 1
	}

	// Parse bounds - handle both "0..4" and "1..5" styles
	lower := 0
	upper := 0
	fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &lower)
	fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &upper)

	if upper >= lower {
		return upper - lower + 1
	}
	return 1
}

type symbolRecord struct {
	info          TagInfo
	guid          [16]byte
	attributes    map[string]string
	extendedFlags uint32
	tail          []byte
	unsupported   string
}

func parseSymbolInfo(data []byte) (*TagInfo, error) {
	budget := parseBudget{remaining: 1000000, maxDepth: 64}
	record, err := parseSymbolRecord(data, &budget)
	if err != nil {
		return nil, err
	}
	return &record.info, nil
}

func parseSymbolRecord(data []byte, budget *parseBudget) (*symbolRecord, error) {
	return parseSymbolRecordFields(data, budget, false)
}

func parseSymbolRecordFields(data []byte, budget *parseBudget, lookup bool) (*symbolRecord, error) {
	if len(data) < 33 || uint64(binary.LittleEndian.Uint32(data[:4])) != uint64(len(data)) {
		return nil, fmt.Errorf("invalid symbol entry length")
	}
	record := &symbolRecord{info: TagInfo{IndexGroup: binary.LittleEndian.Uint32(data[4:8]), IndexOffset: binary.LittleEndian.Uint32(data[8:12]), Size: binary.LittleEndian.Uint32(data[12:16]), TypeCode: mapAdsType(binary.LittleEndian.Uint32(data[16:20])), Flags: binary.LittleEndian.Uint32(data[20:24])}}
	c := metadataCursor{data: data, offset: 30}
	fields := []*string{&record.info.Name, &record.info.TypeName, &record.info.Comment}
	for i, field := range fields {
		length := uint64(binary.LittleEndian.Uint16(data[24+i*2 : 26+i*2]))
		var value string
		var err error
		if i == 0 && lookup && record.info.IndexGroup == 0x4041 && record.info.TypeCode == TypeBool && record.info.Size == 1 && record.info.Flags&SymFlagTypeGUID != 0 {
			// TwinCAT bit-member lookups reserve the requested name's full
			// width but return a shorter storage-parent name, zero padded.
			// The client must validate that alias against the uploaded layout.
			var bytes []byte
			bytes, err = c.take(length + 1)
			if err == nil {
				value = strings.TrimRight(string(bytes), "\x00")
				if bytes[len(bytes)-1] != 0 || strings.ContainsRune(value, 0) {
					err = fmt.Errorf("invalid bit lookup name padding")
				}
			}
		} else {
			value, err = c.text(length)
		}
		if err != nil {
			return nil, err
		}
		*field = value
	}
	if record.info.Name == "" {
		return nil, fmt.Errorf("empty symbol name")
	}
	if record.info.Flags&SymFlagTypeGUID != 0 {
		bytes, err := c.take(16)
		if err != nil {
			return nil, err
		}
		copy(record.guid[:], bytes)
	}
	if record.info.Flags&SymFlagAttributes != 0 {
		attributes, err := parseAttributes(&c, budget)
		if err != nil {
			return nil, err
		}
		record.attributes = attributes
	}
	if record.info.Flags&SymFlagExtendedFlags != 0 {
		bytes, err := c.take(4)
		if err != nil {
			return nil, err
		}
		record.extendedFlags = binary.LittleEndian.Uint32(bytes)
		if record.extendedFlags != 0 {
			record.unsupported = fmt.Sprintf("unsupported symbol extension flags 0x%x", record.extendedFlags)
		}
	}
	if unknown := record.info.Flags & ^uint32(0xffff); unknown != 0 {
		record.unsupported = fmt.Sprintf("unsupported symbol flags 0x%x", unknown)
	}
	if record.info.Flags&(SymFlagItfMethodAccess|SymFlagMethodDeref) != 0 {
		record.unsupported = "symbol requires unsupported method access"
	}
	if c.offset != len(data) {
		record.tail = append([]byte(nil), data[c.offset:]...)
		if record.unsupported == "" {
			if len(data)%8 != 0 || len(data)-c.offset > 7 {
				return nil, fmt.Errorf("unaccounted symbol extension")
			}
			for _, value := range record.tail {
				if value != 0 {
					return nil, fmt.Errorf("nonzero symbol padding")
				}
			}
		}
	}
	if record.info.TypeCode == TypeUnknown {
		record.info.TypeCode = mapTypeFromName(record.info.TypeName, record.info.Size)
	}
	return record, nil
}

func parseSymbolTable(data []byte, count, limit uint32) ([]TagInfo, error) {
	cfg := defaultOptions()
	cfg.maxSymbols = limit
	tags, _, err := parseSymbolRecords(data, count, cfg)
	return tags, err
}

func parseSymbolRecords(data []byte, count uint32, cfg options) ([]TagInfo, map[string]*symbolRecord, error) {
	if count > cfg.maxSymbols || uint64(count)*33 > uint64(len(data)) || uint64(len(data)) > uint64(cfg.maxMetadata) || uint64(count) > uint64(cfg.maxElements) {
		return nil, nil, fmt.Errorf("symbol count/byte/expansion limit or inconsistent sizes")
	}
	budget := parseBudget{remaining: uint64(cfg.maxElements), maxDepth: cfg.maxDepth, deadline: cfg.deadline}
	records := make(map[string]*symbolRecord, int(count))
	tags := make([]TagInfo, 0, int(count))
	offset := 0
	for index := uint32(0); index < count; index++ {
		if err := budget.consume(1); err != nil {
			return nil, nil, err
		}
		if len(data)-offset < 4 {
			return nil, nil, fmt.Errorf("symbol %d header truncated", index)
		}
		size := uint64(binary.LittleEndian.Uint32(data[offset : offset+4]))
		if size < 33 || size > uint64(len(data)-offset) {
			return nil, nil, fmt.Errorf("symbol %d length invalid", index)
		}
		record, err := parseSymbolRecord(data[offset:offset+int(size)], &budget)
		if err != nil {
			return nil, nil, fmt.Errorf("symbol %d: %w", index, err)
		}
		if records[record.info.Name] != nil {
			return nil, nil, fmt.Errorf("duplicate symbol %q", record.info.Name)
		}
		records[record.info.Name] = record
		tags = append(tags, record.info)
		offset += int(size)
	}
	if offset != len(data) {
		return nil, nil, fmt.Errorf("symbol upload trailing bytes")
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return tags, records, nil
}
