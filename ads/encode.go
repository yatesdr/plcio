package ads

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/yatesdr/plcio/metadata"
)

func encodeValue(schema *schemaType, value any, cfg options) ([]byte, error) {
	if schema.size > uint64(cfg.maxPayload) || schema.size > uint64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("value payload limit exceeded")
	}
	bytes, raw := value.([]byte)
	if !raw {
		// Unsupported semantic layouts fail before any access-graph traversal.
		if _, err := valueExpansion(schema, uint64(cfg.maxElements), 1, cfg.maxDepth, cfg.deadline); err != nil {
			return nil, err
		}
	}
	budget := parseBudget{remaining: uint64(cfg.maxElements), maxDepth: cfg.maxDepth, deadline: cfg.deadline}
	readOnly, err := knownReadOnly(schema, &budget)
	if err != nil {
		return nil, err
	}
	if readOnly {
		return nil, fmt.Errorf("whole value contains read-only member storage")
	}
	// Explicit advanced storage input remains available, including opaque layouts.
	// It never establishes semantic validation and must match the complete target.
	if raw {
		if uint64(len(bytes)) != schema.size {
			return nil, fmt.Errorf("raw byte length %d, want %d", len(bytes), schema.size)
		}
		result := append([]byte(nil), bytes...)
		if err := checkDeadline(cfg.deadline); err != nil {
			return nil, err
		}
		return result, nil
	}
	result := make([]byte, int(schema.size))
	if err := encodeInto(schema, value, result, cfg.deadline); err != nil {
		return nil, err
	}
	if err := checkDeadline(cfg.deadline); err != nil {
		return nil, err
	}
	return result, nil
}

// Access checks also protect raw storage writes. Memoization visits shared nodes
// once; the budget counts every edge attempt, including visits to cached nodes.
func knownReadOnly(schema *schemaType, budget *parseBudget) (bool, error) {
	if schema.element == nil && len(schema.members) == 0 {
		return false, budget.consume(1)
	}
	seen := make(map[*schemaType]bool)
	var walk func(*schemaType, uint32) (bool, error)
	walk = func(current *schemaType, depth uint32) (bool, error) {
		if depth > budget.maxDepth {
			return false, fmt.Errorf("access validation depth limit exceeded")
		}
		if err := budget.consume(1); err != nil {
			return false, err
		}
		if seen[current] {
			return false, nil
		}
		seen[current] = true
		if current.element != nil {
			return walk(current.element, depth+1)
		}
		for _, member := range current.members {
			if member.readOnly {
				return true, budget.consume(1)
			}
			readOnly, err := walk(member.typeOf, depth+1)
			if readOnly || err != nil {
				return readOnly, err
			}
		}
		return false, nil
	}
	return walk(schema, 1)
}

func encodeInto(schema *schemaType, value any, data []byte, deadline time.Time) error {
	if len(schema.dimensions) != 0 {
		count, err := countDimensions(schema.dimensions, ^uint64(0))
		if err != nil {
			return err
		}
		values := reflect.ValueOf(value)
		if !values.IsValid() || (values.Kind() != reflect.Slice && values.Kind() != reflect.Array) || uint64(values.Len()) != count {
			return fmt.Errorf("array input %T must contain exactly %d elements", value, count)
		}
		size := int(schema.element.size)
		for i := 0; i < values.Len(); i++ {
			if i%64 == 0 {
				if err := checkDeadline(deadline); err != nil {
					return err
				}
			}
			if err := encodeInto(schema.element, values.Index(i).Interface(), data[i*size:(i+1)*size], deadline); err != nil {
				return fmt.Errorf("element %d: %w", i, err)
			}
		}
		return nil
	}
	if schema.kind == metadata.KindStruct {
		members, ok := value.(map[string]any)
		if !ok || len(members) != len(schema.members) {
			return fmt.Errorf("record input must be a complete map of %d declared members", len(schema.members))
		}
		for _, member := range schema.members {
			if err := checkDeadline(deadline); err != nil {
				return err
			}
			input, exists := members[member.name]
			if !exists {
				return fmt.Errorf("missing record member %q", member.name)
			}
			if member.readOnly {
				return fmt.Errorf("record member %q is read-only", member.name)
			}
			if member.typeOf.bit {
				boolean, err := booleanInput(input)
				if err != nil {
					return fmt.Errorf("member %s: %w", member.name, err)
				}
				if member.sizeBits != 1 {
					return fmt.Errorf("unsupported packed member width")
				}
				if boolean {
					data[member.offsetBits/8] |= 1 << uint(member.offsetBits%8)
				}
			} else {
				if member.offsetBits%8 != 0 || member.sizeBits%8 != 0 {
					return fmt.Errorf("unaligned non-BIT member %s", member.name)
				}
				start, end := int(member.offsetBits/8), int((member.offsetBits+member.sizeBits)/8)
				if err := encodeInto(member.typeOf, input, data[start:end], deadline); err != nil {
					return fmt.Errorf("member %s: %w", member.name, err)
				}
			}
		}
		return nil
	}
	switch schema.kind {
	case metadata.KindBool:
		boolean, err := booleanInput(value)
		if err != nil {
			return err
		}
		if boolean {
			data[0] = 1
		}
		return nil
	case metadata.KindInt, metadata.KindUint:
		bits, err := integerStorage(value, schema.kind, schema.bits)
		if err != nil {
			return err
		}
		if schema.min != nil {
			integer, _ := integerInput(value)
			if integer.Cmp(schema.min) < 0 || integer.Cmp(schema.max) > 0 {
				return fmt.Errorf("integer outside declared subrange [%s,%s]", schema.min, schema.max)
			}
		}
		if schema.kind == metadata.KindUint {
			if err := validateTimeOfDay(schema, bits); err != nil {
				return err
			}
		}
		switch schema.bits {
		case 8:
			data[0] = byte(bits)
		case 16:
			binary.LittleEndian.PutUint16(data, uint16(bits))
		case 32:
			binary.LittleEndian.PutUint32(data, uint32(bits))
		case 64:
			binary.LittleEndian.PutUint64(data, bits)
		default:
			return fmt.Errorf("unsupported integer width")
		}
		return nil
	case metadata.KindFloat:
		number, err := floatInput(value)
		if err != nil {
			return err
		}

		if schema.bits == 32 {
			narrowed := float32(number)
			if math.IsInf(float64(narrowed), 0) && !math.IsInf(number, 0) {
				return fmt.Errorf("REAL overflow")
			}
			binary.LittleEndian.PutUint32(data, math.Float32bits(narrowed))
			return nil
		}
		if schema.bits == 64 {
			binary.LittleEndian.PutUint64(data, math.Float64bits(number))
			return nil
		}
	case metadata.KindString:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("string input required, got %T", value)
		}
		bytes, err := encodeText(text, schema.wide, schema.utf8)
		if err != nil {
			return err
		}
		if len(bytes) > len(data) {
			return fmt.Errorf("string exceeds storage capacity %d bytes", len(data))
		}
		copy(data, bytes)
		return nil
	}
	return fmt.Errorf("unsupported scalar %s", schema.name)
}

// Common integer inputs stay exact without allocating a big integer. The
// exclusive floating upper bound also handles 2^63/2^64 without rounded maxima.
func integerStorage(value any, kind metadata.Kind, width uint16) (uint64, error) {
	if width == 0 || width > 64 {
		return 0, fmt.Errorf("unsupported integer width")
	}
	signed := func(n int64) (uint64, error) {
		if kind == metadata.KindUint {
			if n < 0 || (width < 64 && uint64(n) >= uint64(1)<<width) {
				return 0, fmt.Errorf("integer outside uint%d range", width)
			}
		} else if width < 64 && (n < -(int64(1)<<(width-1)) || n >= int64(1)<<(width-1)) {
			return 0, fmt.Errorf("integer outside int%d range", width)
		}
		return uint64(n), nil
	}
	unsigned := func(n uint64) (uint64, error) {
		bits := width
		if kind == metadata.KindInt {
			bits--
		}
		if bits < 64 && n >= uint64(1)<<bits {
			return 0, fmt.Errorf("integer outside %s%d range", kind, width)
		}
		return n, nil
	}
	switch n := value.(type) {
	case int:
		return signed(int64(n))
	case int8:
		return signed(int64(n))
	case int16:
		return signed(int64(n))
	case int32:
		return signed(int64(n))
	case int64:
		return signed(n)
	case uint:
		return unsigned(uint64(n))
	case uint8:
		return unsigned(uint64(n))
	case uint16:
		return unsigned(uint64(n))
	case uint32:
		return unsigned(uint64(n))
	case uint64:
		return unsigned(n)
	case float32:
		return integerStorage(float64(n), kind, width)
	case float64:
		lower, upper := float64(0), math.Ldexp(1, int(width))
		if kind == metadata.KindInt {
			upper = math.Ldexp(1, int(width)-1)
			lower = -upper
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < lower || n >= upper {
			return 0, fmt.Errorf("integer input must be finite, integral and in %s%d range", kind, width)
		}
		if kind == metadata.KindInt {
			return uint64(int64(n)), nil
		}
		return uint64(n), nil
	default:
		return 0, fmt.Errorf("integer input required, got %T", value)
	}
}

func integerInput(value any) (*big.Int, error) {
	switch v := value.(type) {
	case int:
		return big.NewInt(int64(v)), nil
	case int8:
		return big.NewInt(int64(v)), nil
	case int16:
		return big.NewInt(int64(v)), nil
	case int32:
		return big.NewInt(int64(v)), nil
	case int64:
		return big.NewInt(v), nil
	case uint:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint8:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint16:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint32:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint64:
		return new(big.Int).SetUint64(v), nil
	case float32:
		return integerInput(float64(v))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v {
			return nil, fmt.Errorf("integer input must be finite and integral")
		}
		result, accuracy := new(big.Float).SetFloat64(v).Int(nil)
		if accuracy != big.Exact {
			return nil, fmt.Errorf("inexact integer input")
		}
		return result, nil
	default:
		return nil, fmt.Errorf("integer input required, got %T", value)
	}
}

func booleanInput(value any) (bool, error) {
	if boolean, ok := value.(bool); ok {
		return boolean, nil
	}
	integer, err := integerInput(value)
	if err != nil {
		return false, err
	}
	if integer.Sign() == 0 {
		return false, nil
	}
	if integer.Cmp(big.NewInt(1)) == 0 {
		return true, nil
	}
	return false, fmt.Errorf("BOOL numeric input must be 0 or 1")
}

func floatInput(value any) (float64, error) {
	switch v := value.(type) {
	case float32:
		return float64(v), nil
	case float64:
		return v, nil
	}
	integer, err := integerInput(value)
	if err != nil {
		return 0, err
	}
	number, accuracy := new(big.Float).SetInt(integer).Float64()
	if accuracy != big.Exact {
		return 0, fmt.Errorf("integer cannot be represented exactly as floating point")
	}
	return number, nil
}

func encodeText(text string, wide, utf8Storage bool) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("invalid UTF-8 input")
	}
	for _, r := range text {
		if r == 0 {
			return nil, fmt.Errorf("embedded NUL in string input")
		}
	}
	if wide {
		result := make([]byte, 0, (utf8.RuneCountInString(text)+1)*2)
		for _, r := range text {
			if r > 0xffff || (r >= 0xd800 && r <= 0xdfff) {
				return nil, fmt.Errorf("character U+%04X is not representable in UCS-2", r)
			}
			result = append(result, byte(r), byte(r>>8))
		}
		return append(result, 0, 0), nil
	}
	if utf8Storage {
		return append([]byte(text), 0), nil
	}
	result := make([]byte, 0, utf8.RuneCountInString(text)+1)
	for _, r := range text {
		if r > 255 {
			return nil, fmt.Errorf("character U+%04X is not representable in Latin-1", r)
		}
		result = append(result, byte(r))
	}
	return append(result, 0), nil
}
