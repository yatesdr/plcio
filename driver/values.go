package driver

// normalizeDecoded widens values whose decoder has established their category.
// Opaque []byte and []int buffers stay intact; record primitive arrays arrive as
// []any and can be normalized without interpreting an opaque buffer as a field.
func normalizeDecoded(value any) any {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case uint:
		return uint64(v)
	case uint8:
		return uint64(v)
	case uint16:
		return uint64(v)
	case uint32:
		return uint64(v)
	case float32:
		return float64(v)
	case []int8:
		return widenSlice(v, func(n int8) int64 { return int64(n) })
	case []int16:
		return widenSlice(v, func(n int16) int64 { return int64(n) })
	case []int32:
		return widenSlice(v, func(n int32) int64 { return int64(n) })
	case []uint16:
		return widenSlice(v, func(n uint16) uint64 { return uint64(n) })
	case []uint32:
		return widenSlice(v, func(n uint32) uint64 { return uint64(n) })
	case []float32:
		return widenSlice(v, func(n float32) float64 { return float64(n) })
	case map[string]any:
		result := make(map[string]any, len(v))
		for name, field := range v {
			result[name] = normalizeDecoded(field)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, element := range v {
			result[i] = normalizeDecoded(element)
		}
		if len(result) == 0 {
			return result
		}
		switch result[0].(type) {
		case bool:
			if typed, ok := homogeneousSlice[bool](result); ok {
				return typed
			}
		case int64:
			if typed, ok := homogeneousSlice[int64](result); ok {
				return typed
			}
		case uint64:
			if typed, ok := homogeneousSlice[uint64](result); ok {
				return typed
			}
		case float64:
			if typed, ok := homogeneousSlice[float64](result); ok {
				return typed
			}
		case string:
			if typed, ok := homogeneousSlice[string](result); ok {
				return typed
			}
		}
		return result
	default:
		return value
	}
}

func widenSlice[S, T any](values []S, convert func(S) T) []T {
	if values == nil {
		return nil
	}
	result := make([]T, len(values))
	for i, value := range values {
		result[i] = convert(value)
	}
	return result
}

func homogeneousSlice[T any](values []any) ([]T, bool) {
	for _, value := range values {
		if _, ok := value.(T); !ok {
			return nil, false
		}
	}
	result := make([]T, len(values))
	for i, value := range values {
		result[i] = value.(T)
	}
	return result, true
}

// Byte/int slice conversion is permitted only at a known atomic decoder root.
func normalizePrimitive(value any) any {
	switch v := value.(type) {
	case []byte:
		return widenSlice(v, func(n byte) uint64 { return uint64(n) })
	case []int:
		return widenSlice(v, func(n int) int64 { return int64(n) })
	default:
		return normalizeDecoded(value)
	}
}
