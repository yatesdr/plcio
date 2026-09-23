package ads

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/yatesdr/plcio/metadata"
)

func valueExpansion(schema *schemaType, limit uint64, depth, maxDepth uint32, deadline time.Time) (uint64, error) {
	if err := checkDeadline(deadline); err != nil {
		return 0, err
	}
	if limit == 0 {
		return 0, fmt.Errorf("value expansion limit exceeded")
	}
	if depth > maxDepth {
		return 0, fmt.Errorf("value depth limit exceeded")
	}
	if schema.unsupported != "" {
		return 0, fmt.Errorf("unsupported %s: %s", schema.name, schema.unsupported)
	}
	cost := uint64(1)
	if len(schema.dimensions) != 0 {
		count, err := countDimensions(schema.dimensions, limit)
		if err != nil {
			return 0, err
		}
		elementCost, err := valueExpansion(schema.element, (limit-1)/count, depth+1, maxDepth, deadline)
		if err != nil {
			return 0, err
		}
		if count > (limit-1)/elementCost {
			return 0, fmt.Errorf("value expansion limit exceeded")
		}
		return 1 + count*elementCost, nil
	}
	for _, member := range schema.members {
		memberCost, err := valueExpansion(member.typeOf, limit-cost, depth+1, maxDepth, deadline)
		if err != nil {
			return 0, err
		}
		if memberCost > limit-cost {
			return 0, fmt.Errorf("value expansion limit exceeded")
		}
		cost += memberCost
	}
	return cost, nil
}

func decodeValue(schema *schemaType, data []byte, cfg options) (any, error) {
	if uint64(len(data)) != schema.size {
		return nil, fmt.Errorf("%s buffer size %d, want %d", schema.name, len(data), schema.size)
	}
	if uint64(len(data)) > uint64(cfg.maxPayload) {
		return nil, fmt.Errorf("value payload limit exceeded")
	}
	if _, err := valueExpansion(schema, uint64(cfg.maxElements), 1, cfg.maxDepth, cfg.deadline); err != nil {
		return nil, err
	}
	value, err := decodeValueUnchecked(schema, data, cfg.deadline)
	if err == nil {
		err = checkDeadline(cfg.deadline)
	}
	if err != nil {
		return nil, err
	}
	return value, err
}

func decodeValueUnchecked(schema *schemaType, data []byte, deadline time.Time) (any, error) {
	if len(schema.dimensions) != 0 {
		count, _ := countDimensions(schema.dimensions, ^uint64(0))
		n := int(count)
		elementSize := int(schema.element.size)
		var result any
		switch schema.kind {
		case metadata.KindBool:
			result = make([]bool, n)
		case metadata.KindInt:
			result = make([]int64, n)
		case metadata.KindUint:
			result = make([]uint64, n)
		case metadata.KindFloat:
			result = make([]float64, n)
		case metadata.KindString:
			result = make([]string, n)
		case metadata.KindStruct:
			result = make([]any, n)
		default:
			return nil, fmt.Errorf("unsupported array element")
		}
		for i := range n {
			if i%64 == 0 {
				if err := checkDeadline(deadline); err != nil {
					return nil, err
				}
			}
			value, err := decodeValueUnchecked(schema.element, data[i*elementSize:(i+1)*elementSize], deadline)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			switch values := result.(type) {
			case []bool:
				values[i] = value.(bool)
			case []int64:
				values[i] = value.(int64)
			case []uint64:
				values[i] = value.(uint64)
			case []float64:
				values[i] = value.(float64)
			case []string:
				values[i] = value.(string)
			case []any:
				values[i] = value
			}
		}
		return result, nil
	}
	if schema.kind == metadata.KindStruct {
		result := make(map[string]any, len(schema.members))
		for _, member := range schema.members {
			if err := checkDeadline(deadline); err != nil {
				return nil, err
			}
			var value any
			var err error
			if member.typeOf.bit {
				if member.sizeBits != 1 || member.typeOf.kind != metadata.KindBool {
					return nil, fmt.Errorf("unsupported packed member %s", member.name)
				}
				value = data[member.offsetBits/8]&(1<<uint(member.offsetBits%8)) != 0
			} else {
				if member.offsetBits%8 != 0 || member.sizeBits%8 != 0 {
					return nil, fmt.Errorf("unaligned non-BIT member %s", member.name)
				}
				start, end := int(member.offsetBits/8), int((member.offsetBits+member.sizeBits)/8)
				value, err = decodeValueUnchecked(member.typeOf, data[start:end], deadline)
				if err != nil {
					return nil, fmt.Errorf("member %s: %w", member.name, err)
				}
			}
			result[member.name] = value
		}
		return result, nil
	}
	switch schema.kind {
	case metadata.KindBool:
		if schema.bit {
			return data[0]&1 != 0, nil
		}
		return data[0] != 0, nil
	case metadata.KindInt:
		switch schema.bits {
		case 8:
			return int64(int8(data[0])), nil
		case 16:
			return int64(int16(binary.LittleEndian.Uint16(data))), nil
		case 32:
			return int64(int32(binary.LittleEndian.Uint32(data))), nil
		case 64:
			return int64(binary.LittleEndian.Uint64(data)), nil
		}
	case metadata.KindUint:
		var value uint64
		switch schema.bits {
		case 8:
			value = uint64(data[0])
		case 16:
			value = uint64(binary.LittleEndian.Uint16(data))
		case 32:
			value = uint64(binary.LittleEndian.Uint32(data))
		case 64:
			value = binary.LittleEndian.Uint64(data)
		default:
			return nil, fmt.Errorf("unsupported unsigned width")
		}
		// TOD/LTOD reads are not range-checked: the raw count since midnight
		// is returned even when it is >= 24h (a PLC can hold such a value, e.g.
		// after arithmetic), so a successful read never becomes an error.
		// Writes still reject out-of-range values in encode.go.
		return value, nil
	case metadata.KindFloat:
		if schema.bits == 32 {
			return float64(math.Float32frombits(binary.LittleEndian.Uint32(data))), nil
		}
		if schema.bits == 64 {
			return math.Float64frombits(binary.LittleEndian.Uint64(data)), nil
		}
	case metadata.KindString:
		return decodeText(data, schema.wide, schema.utf8)
	}
	return nil, fmt.Errorf("unsupported scalar %s", schema.name)
}

func validateTimeOfDay(schema *schemaType, value uint64) error {
	if schema.timeOfDayLimit != 0 && value >= schema.timeOfDayLimit {
		return fmt.Errorf("time of day outside [0,%d) %s", schema.timeOfDayLimit, schema.unit)
	}
	return nil
}

// TwinCAT WSTRING is UCS-2 (not surrogate-pair UTF-16). STRING defaults to
// Latin-1; a published TcEncoding UTF-8 attribute selects UTF-8 storage.
func decodeText(data []byte, wide, utf8Storage bool) (string, error) {
	if wide {
		if len(data) < 2 || len(data)%2 != 0 {
			return "", fmt.Errorf("invalid WSTRING storage size")
		}
		result := make([]rune, 0, len(data)/2)
		for i := 0; i < len(data); i += 2 {
			value := binary.LittleEndian.Uint16(data[i : i+2])
			if value == 0 {
				return string(result), nil
			}
			if value >= 0xd800 && value <= 0xdfff {
				return "", fmt.Errorf("invalid UCS-2 code point")
			}
			result = append(result, rune(value))
		}
		return "", fmt.Errorf("unterminated WSTRING")
	}
	end := 0
	for end < len(data) && data[end] != 0 {
		end++
	}
	if end == len(data) {
		return "", fmt.Errorf("unterminated STRING")
	}
	if utf8Storage {
		if !utf8.Valid(data[:end]) {
			return "", fmt.Errorf("invalid UTF-8 STRING")
		}
		return string(data[:end]), nil
	}
	runes := make([]rune, end)
	for i, value := range data[:end] {
		runes[i] = rune(value)
	}
	return string(runes), nil
}
