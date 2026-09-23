package omron

// NJ/NX (EtherNet/IP) value handling: the Read Tag reply layout, the
// vendor-specific data type codes, the 8-byte nanosecond time types, and the
// strict integer conversions used by EncodeValue.
//
// References:
//   - W506 (NJ/NX-series CPU Unit Built-in EtherNet/IP Port User's Manual)
//     7-6-1 Read Service for Variables, 7-7-1 Data Type Codes, 7-7-3/7-7-4
//     data formats, appendix A (variable memory sizes).
//   - W501 (NJ/NX-series CPU Unit Software User's Manual) section 6-3 data
//     type ranges.
//   - aphyt (github.com/aphyt/aphytcomm): cip/cip.py CIPCommonFormat and
//     omron/omron_datatypes.py.

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"time"
)

const nsPerDay int64 = 24 * 60 * 60 * int64(time.Second)

// Upper limits from W501 section 6-3: DATE D#2106-02-06, DATE_AND_TIME
// DT#2106-02-06-23:59:59.999999999 (both start at 1970-01-01).
var (
	njMaxDateNs     = time.Date(2106, 2, 6, 0, 0, 0, 0, time.UTC).UnixNano()
	njMaxDateTimeNs = time.Date(2106, 2, 6, 23, 59, 59, 999999999, time.UTC).UnixNano()
)

// cipTypeFromWire converts the data type and AddInfo length bytes of an NJ/NX
// Read Tag reply to this package's type code. Single-byte codes below 0x80
// are Omron vendor-specific codes (W506 7-7-1: 04..0C hex) and are moved to
// 0x0100|code so they cannot be mistaken for the FINS pseudo-types. Other codes
// keep their historical value (low byte = code, high byte = AddInfo length),
// so an abbreviated structure stays 0x02A0 (see TypeStructFlag).
func cipTypeFromWire(code, addInfoLen byte) uint16 {
	if addInfoLen == 0 && code < 0x80 {
		return omronVendorTypeBase | uint16(code)
	}
	return uint16(code) | uint16(addInfoLen)<<8
}

// cipWireType is the inverse of cipTypeFromWire for the two bytes that start
// the Write Tag service data (data type, AddInfo length).
func cipWireType(typeCode uint16) []byte {
	if typeCode&0xFF00 == omronVendorTypeBase {
		return []byte{byte(typeCode), 0}
	}
	return binary.LittleEndian.AppendUint16(nil, typeCode)
}

// discoveryTypeCode maps a type code reported by tag discovery the same way
// as a Read Tag reply, preserving the array flag.
func discoveryTypeCode(code uint16) uint16 {
	base := BaseType(code)
	if base == 0 || base >= 0x80 {
		return code
	}
	return code&TypeArrayFlag | omronVendorTypeBase | base
}

// parseCIPReadReply splits NJ/NX Read Tag reply data (after the CIP reply
// header) per W506 7-6-1: data type (USINT), AddInfo length (USINT), AddInfo
// (the structure CRC for structures), then the actual data.
func parseCIPReadReply(resp []byte) (uint16, []byte, error) {
	if len(resp) < 2 {
		return 0, nil, fmt.Errorf("response data too short: %d bytes", len(resp))
	}
	addInfoLen := int(resp[1])
	if 2+addInfoLen > len(resp) {
		return 0, nil, fmt.Errorf("response truncated: AddInfo length %d with %d bytes", addInfoLen, len(resp)-2)
	}
	return cipTypeFromWire(resp[0], resp[1]), resp[2+addInfoLen:], nil
}

// checkCIPReadData rejects reply data that cannot be the full value of its
// type (too short, truncated STRING, out-of-range NJ time value), so a short
// or invalid reply surfaces as an error instead of a zero or clipped value.
func checkCIPReadData(typeCode uint16, data []byte) error {
	base := BaseType(typeCode)
	if base&TypeStructFlag == TypeStructFlag {
		return nil
	}
	switch base {
	case TypeCIPBool:
		if len(data) < 1 {
			return fmt.Errorf("BOOL reply has no data")
		}
		return nil
	case TypeCIPSTRING:
		if len(data) < 2 {
			return fmt.Errorf("STRING reply too short: %d bytes", len(data))
		}
		if n := int(binary.LittleEndian.Uint16(data)); n > len(data)-2 {
			return fmt.Errorf("STRING reply truncated: length %d with %d bytes", n, len(data)-2)
		}
		return nil
	}
	if size := TypeSize(base); size > 0 && len(data) < size {
		return fmt.Errorf("%s reply too short: %d bytes, need %d", TypeName(base), len(data), size)
	}
	return checkNJValue(base, data)
}

// checkNJValue validates an NJ/NX time value against the W501 ranges.
func checkNJValue(base uint16, data []byte) error {
	if len(data) < 8 {
		return nil
	}
	u := binary.LittleEndian.Uint64(data)
	switch base {
	case TypeOmronTOD:
		if u >= uint64(nsPerDay) {
			return fmt.Errorf("invalid TIME_OF_DAY %d ns (must be below %d)", u, nsPerDay)
		}
	case TypeOmronDate:
		if u > uint64(njMaxDateNs) {
			return fmt.Errorf("invalid DATE %d ns (after D#2106-02-06)", u)
		}
	case TypeOmronDT:
		if u > uint64(njMaxDateTimeNs) {
			return fmt.Errorf("invalid DATE_AND_TIME %d ns (after DT#2106-02-06-23:59:59.999999999)", u)
		}
	}
	return nil
}

// decodeNJValue decodes the NJ/NX-only types: TIME and TIME_OF_DAY as int64
// nanoseconds, DATE and DATE_AND_TIME as a UTC time.Time carrying the
// controller's wall clock, enumerations as int32. ok is false when data is
// too short or the value cannot be represented.
func decodeNJValue(base uint16, data []byte) (interface{}, bool) {
	if base == TypeOmronEnum {
		if len(data) < 4 {
			return nil, false
		}
		return int32(binary.LittleEndian.Uint32(data)), true
	}
	if len(data) < 8 {
		return nil, false
	}
	u := binary.LittleEndian.Uint64(data)
	switch base {
	case TypeOmronTime, TypeOmronTimeNSec:
		return int64(u), true
	case TypeOmronTOD:
		if u > math.MaxInt64 {
			return nil, false
		}
		return int64(u), true
	case TypeOmronDate, TypeOmronDT:
		if u > math.MaxInt64 {
			return nil, false
		}
		return time.Unix(0, int64(u)).UTC(), true
	}
	return nil, false
}

// encodeNJTime encodes TIME, TIME_OF_DAY, DATE or DATE_AND_TIME as 8 bytes of
// little-endian nanoseconds. TIME and TIME_OF_DAY take a time.Duration or an
// integer count of nanoseconds; DATE and DATE_AND_TIME take a time.Time (its
// wall clock in its own location is stored, since the controller clock has no
// zone) or integer nanoseconds since 1970-01-01. Values outside the W501
// ranges, and DATE values that are not midnight, are rejected.
func encodeNJTime(value interface{}, base uint16) ([]byte, error) {
	name := TypeName(base)
	var ns int64
	switch base {
	case TypeOmronTime, TypeOmronTimeNSec, TypeOmronTOD:
		if _, isTime := value.(time.Time); isTime {
			return nil, fmt.Errorf("%s: cannot convert time.Time (expected time.Duration or nanoseconds)", name)
		}
		n, err := signedValue(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w (expected time.Duration or nanoseconds)", name, err)
		}
		ns = n
		if base == TypeOmronTOD && (ns < 0 || ns >= nsPerDay) {
			return nil, fmt.Errorf("TIME_OF_DAY: %v outside 00:00:00..23:59:59.999999999", time.Duration(ns))
		}
	case TypeOmronDate, TypeOmronDT:
		if t, ok := value.(time.Time); ok {
			wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
			if wall.Year() < 1970 || wall.Year() > 2106 {
				return nil, fmt.Errorf("%s: %v outside 1970-01-01..2106-02-06", name, t)
			}
			ns = wall.UnixNano()
		} else {
			if _, isDur := value.(time.Duration); isDur {
				return nil, fmt.Errorf("%s: cannot convert time.Duration (expected time.Time or nanoseconds since 1970-01-01)", name)
			}
			n, err := signedValue(value, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %w (expected time.Time or nanoseconds since 1970-01-01)", name, err)
			}
			ns = n
		}
		limit := njMaxDateTimeNs
		if base == TypeOmronDate {
			limit = njMaxDateNs
		}
		if ns < 0 || ns > limit {
			return nil, fmt.Errorf("%s: %s outside the NJ/NX range 1970-01-01..%s", name,
				time.Unix(0, ns).UTC().Format(time.RFC3339Nano), time.Unix(0, limit).UTC().Format(time.RFC3339Nano))
		}
		if base == TypeOmronDate && ns%nsPerDay != 0 {
			return nil, fmt.Errorf("DATE: %s has a time-of-day part; DATE stores whole days", time.Unix(0, ns).UTC().Format(time.RFC3339Nano))
		}
	default:
		return nil, fmt.Errorf("unsupported type code: %s", name)
	}
	return binary.LittleEndian.AppendUint64(nil, uint64(ns)), nil
}

// signedValue converts any Go integer kind (including named types such as
// time.Duration) or a whole, finite float to an int64 that fits in a signed
// integer of the given width.
func signedValue(value interface{}, bits uint) (int64, error) {
	lo, hi := int64(-1)<<(bits-1), int64(uint64(1)<<(bits-1)-1)
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n < lo || n > hi {
			return 0, fmt.Errorf("value %d out of range %d..%d", n, lo, hi)
		}
		return n, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u > uint64(hi) {
			return 0, fmt.Errorf("value %d out of range %d..%d", u, lo, hi)
		}
		return int64(u), nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
			return 0, fmt.Errorf("value %v is not a whole number", f)
		}
		// float64(hi) rounds up to 2^(bits-1) for 64 bits, hence >=.
		if f < float64(lo) || f >= -float64(lo) {
			return 0, fmt.Errorf("value %v out of range %d..%d", f, lo, hi)
		}
		return int64(f), nil
	}
	return 0, fmt.Errorf("cannot convert %T to an integer", value)
}

// unsignedValue converts any Go integer kind or a whole, finite float to a
// uint64 that fits in an unsigned integer of the given width.
func unsignedValue(value interface{}, bits uint) (uint64, error) {
	hi := uint64(math.MaxUint64)
	if bits < 64 {
		hi = uint64(1)<<bits - 1
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n < 0 || uint64(n) > hi {
			return 0, fmt.Errorf("value %d out of range 0..%d", n, hi)
		}
		return uint64(n), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u > hi {
			return 0, fmt.Errorf("value %d out of range 0..%d", u, hi)
		}
		return u, nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
			return 0, fmt.Errorf("value %v is not a whole number", f)
		}
		if f < 0 || f >= math.Ldexp(1, int(bits)) {
			return 0, fmt.Errorf("value %v out of range 0..%d", f, hi)
		}
		return uint64(f), nil
	}
	return 0, fmt.Errorf("cannot convert %T to an integer", value)
}
