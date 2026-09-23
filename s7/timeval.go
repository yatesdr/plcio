package s7

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"time"
	"unicode/utf16"
)

// Codecs for the Siemens date/time types and WSTRING text. Every encoder is
// strict: a value that cannot be stored exactly (out of range, sub-unit
// precision, invalid calendar date) is rejected instead of being rounded,
// clamped or truncated.

// Value ranges from the Siemens type documentation.
const (
	maxTODMillis  = 24*60*60*1000 - 1 // TIME_OF_DAY: TOD#23:59:59.999
	maxDateDays   = 65378             // DATE: D#2168-12-31
	maxS5TimeBCD  = 999               // S5TIME: 3 BCD digits
	maxS5TimeMSec = 999 * 10000       // S5TIME: 999 x 10 s = 2h46m30s
)

// s5TimeBases are the S5TIME time bases in milliseconds, indexed by the
// 2-bit time-base code (bits 13-12): 00 = 10 ms, 01 = 100 ms, 10 = 1 s, 11 = 10 s.
var s5TimeBases = [4]int64{10, 100, 1000, 10000}

// dateEpoch is day 0 of the S7 DATE type.
var dateEpoch = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)

// integerValue converts a Go integer (any kind, including named types such as
// time.Duration) or an integral float64/float32 to int64. It fails for
// fractional or non-finite floats and for uint64 values above MaxInt64.
func integerValue(value interface{}) (int64, error) {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u > math.MaxInt64 {
			return 0, fmt.Errorf("value %d out of range", u)
		}
		return int64(u), nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < -(1<<63) || f >= 1<<63 {
			return 0, fmt.Errorf("value %v is not a whole number", f)
		}
		return int64(f), nil
	}
	return 0, fmt.Errorf("cannot convert %T to an integer", value)
}

// millisValue converts a duration-like value to whole milliseconds.
// time.Duration must be a whole number of milliseconds; any other integer
// (or integral float) is taken as milliseconds.
func millisValue(value interface{}, typeName string) (int64, error) {
	if d, ok := value.(time.Duration); ok {
		if d%time.Millisecond != 0 {
			return 0, fmt.Errorf("%s: duration %v has sub-millisecond precision; %s stores whole milliseconds", typeName, d, typeName)
		}
		return int64(d / time.Millisecond), nil
	}
	ms, err := integerValue(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w (expected milliseconds or time.Duration)", typeName, err)
	}
	return ms, nil
}

// encodeTime encodes S7 TIME: signed 32-bit milliseconds, big-endian.
func encodeTime(value interface{}) ([]byte, error) {
	ms, err := millisValue(value, "TIME")
	if err != nil {
		return nil, err
	}
	if ms < math.MinInt32 || ms > math.MaxInt32 {
		return nil, fmt.Errorf("TIME: %d ms outside T#-24D20H31M23S648MS..T#24D20H31M23S647MS", ms)
	}
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(int32(ms)))
	return buf, nil
}

// encodeTimeOfDay encodes S7 TIME_OF_DAY: unsigned 32-bit milliseconds since
// midnight (0 .. 86399999), big-endian.
func encodeTimeOfDay(value interface{}) ([]byte, error) {
	ms, err := millisValue(value, "TIME_OF_DAY")
	if err != nil {
		return nil, err
	}
	if ms < 0 || ms > maxTODMillis {
		return nil, fmt.Errorf("TIME_OF_DAY: %d ms outside 0..%d (00:00:00.000..23:59:59.999)", ms, maxTODMillis)
	}
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(ms))
	return buf, nil
}

// encodeDate encodes S7 DATE: unsigned 16-bit days since 1990-01-01.
// Accepts a day count or a time.Time at midnight (its own location's wall
// clock); the calendar date is stored.
func encodeDate(value interface{}) ([]byte, error) {
	var days int64
	if t, ok := value.(time.Time); ok {
		if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
			return nil, fmt.Errorf("DATE: %v has a time-of-day part; DATE stores whole days", t)
		}
		wall := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		days = int64(wall.Sub(dateEpoch) / (24 * time.Hour))
	} else {
		var err error
		if days, err = integerValue(value); err != nil {
			return nil, fmt.Errorf("DATE: %w (expected days since 1990-01-01 or time.Time)", err)
		}
	}
	if days < 0 || days > maxDateDays {
		return nil, fmt.Errorf("DATE: day %d outside 0..%d (D#1990-01-01..D#2168-12-31)", days, maxDateDays)
	}
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(days))
	return buf, nil
}

// decodeS5Time decodes an S5TIME word to milliseconds.
func decodeS5Time(b []byte) (int64, error) {
	if len(b) < 2 {
		return 0, fmt.Errorf("insufficient data for S5TIME")
	}
	w := binary.BigEndian.Uint16(b)
	if w&0xC000 != 0 {
		return 0, fmt.Errorf("invalid S5TIME 0x%04X: bits 15-14 must be 0", w)
	}
	d2, d1, d0 := int64(w>>8&0x0F), int64(w>>4&0x0F), int64(w&0x0F)
	if d2 > 9 || d1 > 9 || d0 > 9 {
		return 0, fmt.Errorf("invalid S5TIME 0x%04X: not BCD", w)
	}
	return (d2*100 + d1*10 + d0) * s5TimeBases[w>>12&0x03], nil
}

// encodeS5Time encodes milliseconds as S5TIME, choosing the finest time base
// that stores the value exactly. Values that no time base can represent
// exactly (e.g. 10005 ms) or that exceed 2h46m30s are rejected.
func encodeS5Time(value interface{}) ([]byte, error) {
	ms, err := millisValue(value, "S5TIME")
	if err != nil {
		return nil, err
	}
	if ms < 0 || ms > maxS5TimeMSec {
		return nil, fmt.Errorf("S5TIME: %d ms outside 0..%d (S5T#0MS..S5T#2H46M30S)", ms, maxS5TimeMSec)
	}
	for base, unit := range s5TimeBases {
		if ms%unit != 0 || ms/unit > maxS5TimeBCD {
			continue
		}
		n := uint16(ms / unit)
		w := uint16(base)<<12 | (n/100)<<8 | (n/10%10)<<4 | n%10
		return []byte{byte(w >> 8), byte(w)}, nil
	}
	return nil, fmt.Errorf("S5TIME: %d ms cannot be stored exactly (resolution is 10 ms up to 9.99 s, 100 ms up to 99.9 s, 1 s up to 999 s, 10 s above)", ms)
}

func fromBCD(b byte) (int, bool) {
	hi, lo := int(b>>4), int(b&0x0F)
	return hi*10 + lo, hi <= 9 && lo <= 9
}

func toBCD(n int) byte { return byte(n/10<<4 | n%10) }

// decodeDateAndTime decodes an 8-byte BCD DATE_AND_TIME. Layout: year
// (90-99 = 1990-1999, 00-89 = 2000-2089), month, day, hour, minute, second,
// then 3 millisecond digits and a 1-digit weekday (1 = Sunday). The PLC value
// carries no time zone; it is returned as a UTC time.Time with the same wall
// clock.
func decodeDateAndTime(b []byte) (time.Time, error) {
	if len(b) < 8 {
		return time.Time{}, fmt.Errorf("insufficient data for DATE_AND_TIME")
	}
	var f [6]int
	for i := range f {
		v, ok := fromBCD(b[i])
		if !ok {
			return time.Time{}, fmt.Errorf("invalid DATE_AND_TIME % X: byte %d is not BCD", b[:8], i)
		}
		f[i] = v
	}
	msHi, ok := fromBCD(b[6])
	msLo := int(b[7] >> 4)
	if !ok || msLo > 9 {
		return time.Time{}, fmt.Errorf("invalid DATE_AND_TIME % X: milliseconds are not BCD", b[:8])
	}
	year := 2000 + f[0]
	if f[0] >= 90 {
		year = 1900 + f[0]
	}
	t := time.Date(year, time.Month(f[1]), f[2], f[3], f[4], f[5], (msHi*10+msLo)*int(time.Millisecond), time.UTC)
	if t.Month() != time.Month(f[1]) || t.Day() != f[2] || t.Hour() != f[3] || t.Minute() != f[4] || t.Second() != f[5] {
		return time.Time{}, fmt.Errorf("invalid DATE_AND_TIME % X: not a valid date/time", b[:8])
	}
	return t, nil
}

// encodeDateAndTime encodes a time.Time (wall clock in its own location) as
// DATE_AND_TIME. The range is 1990-01-01 .. 2089-12-31 23:59:59.999 and the
// value must be a whole number of milliseconds.
func encodeDateAndTime(value interface{}) ([]byte, error) {
	t, ok := value.(time.Time)
	if !ok {
		return nil, fmt.Errorf("DATE_AND_TIME: cannot convert %T (expected time.Time)", value)
	}
	if t.Year() < 1990 || t.Year() > 2089 {
		return nil, fmt.Errorf("DATE_AND_TIME: year %d outside 1990..2089", t.Year())
	}
	if t.Nanosecond()%int(time.Millisecond) != 0 {
		return nil, fmt.Errorf("DATE_AND_TIME: %v has sub-millisecond precision; DATE_AND_TIME stores whole milliseconds", t)
	}
	ms := t.Nanosecond() / int(time.Millisecond)
	return []byte{
		toBCD(t.Year() % 100), toBCD(int(t.Month())), toBCD(t.Day()),
		toBCD(t.Hour()), toBCD(t.Minute()), toBCD(t.Second()),
		toBCD(ms / 10), byte(ms%10)<<4 | byte(t.Weekday()+1),
	}, nil
}

// decodeDTL decodes a 12-byte DTL: year (UInt), month, day, weekday
// (1 = Sunday), hour, minute, second (USInt each), nanoseconds (UDInt). The
// value carries no time zone; it is returned as UTC with the same wall clock.
func decodeDTL(b []byte) (time.Time, error) {
	if len(b) < 12 {
		return time.Time{}, fmt.Errorf("insufficient data for DTL")
	}
	year := int(binary.BigEndian.Uint16(b[0:2]))
	month, day, hour, minute, second := int(b[2]), int(b[3]), int(b[5]), int(b[6]), int(b[7])
	nsec := int(binary.BigEndian.Uint32(b[8:12]))
	t := time.Date(year, time.Month(month), day, hour, minute, second, nsec, time.UTC)
	if nsec > 999999999 || t.Year() != year || t.Month() != time.Month(month) || t.Day() != day ||
		t.Hour() != hour || t.Minute() != minute || t.Second() != second {
		return time.Time{}, fmt.Errorf("invalid DTL % X: not a valid date/time", b[:12])
	}
	return t, nil
}

// DTL range: DTL#1970-01-01-00:00:00.0 .. DTL#2262-04-11-23:47:16.854775807.
var (
	dtlMin = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	dtlMax = time.Date(2262, 4, 11, 23, 47, 16, 854775807, time.UTC)
)

// encodeDTL encodes a time.Time (wall clock in its own location, nanosecond
// precision) as DTL, including the weekday byte (1 = Sunday).
func encodeDTL(value interface{}) ([]byte, error) {
	t, ok := value.(time.Time)
	if !ok {
		return nil, fmt.Errorf("DTL: cannot convert %T (expected time.Time)", value)
	}
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	if wall.Before(dtlMin) || wall.After(dtlMax) {
		return nil, fmt.Errorf("DTL: %v outside 1970-01-01..2262-04-11 23:47:16.854775807", t)
	}
	buf := make([]byte, 12)
	binary.BigEndian.PutUint16(buf[0:2], uint16(t.Year()))
	buf[2], buf[3], buf[4] = byte(t.Month()), byte(t.Day()), byte(t.Weekday()+1)
	buf[5], buf[6], buf[7] = byte(t.Hour()), byte(t.Minute()), byte(t.Second())
	binary.BigEndian.PutUint32(buf[8:12], uint32(t.Nanosecond()))
	return buf, nil
}

// decodeWString decodes one WSTRING (2-byte max length, 2-byte actual length
// in UTF-16 code units, then UTF-16BE text) including surrogate pairs.
func decodeWString(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	n := int(binary.BigEndian.Uint16(b[2:4]))
	if avail := (len(b) - 4) / 2; n > avail {
		n = avail
	}
	units := make([]uint16, n)
	for i := range units {
		units[i] = binary.BigEndian.Uint16(b[4+2*i:])
	}
	return string(utf16.Decode(units))
}

// encodeWStringWithMaxLen encodes s as a WSTRING whose DB header declares
// maxLen UTF-16 code units. Text longer than maxLen code units (characters
// outside the BMP count as two) is rejected rather than truncated.
func encodeWStringWithMaxLen(s string, maxLen int) ([]byte, error) {
	units := utf16.Encode([]rune(s))
	if len(units) > maxLen {
		return nil, fmt.Errorf("string of %d UTF-16 code units exceeds the WSTRING's maximum length %d (from its DB header)", len(units), maxLen)
	}
	// S7 WSTRING format: [maxLen(2)][actualLen(2)][UTF-16BE code units padded to maxLen]
	result := make([]byte, 4+maxLen*2)
	binary.BigEndian.PutUint16(result[0:2], uint16(maxLen))
	binary.BigEndian.PutUint16(result[2:4], uint16(len(units)))
	for i, u := range units {
		binary.BigEndian.PutUint16(result[4+i*2:], u)
	}
	return result, nil
}

// decodeCheck reports a value the PLC returned that is not a valid instance
// of its type (bad BCD, impossible date). Such reads fail instead of
// producing a plausible-looking wrong value.
func decodeCheck(dataType uint16, data []byte) error {
	base := BaseType(dataType)
	size := TypeSize(base)
	switch base {
	case TypeS5Time, TypeDateAndTime, TypeDTL:
	default:
		return nil
	}
	for off := 0; off+size <= len(data); off += size {
		var err error
		switch base {
		case TypeS5Time:
			_, err = decodeS5Time(data[off:])
		case TypeDateAndTime:
			_, err = decodeDateAndTime(data[off:])
		case TypeDTL:
			_, err = decodeDTL(data[off:])
		}
		if err != nil {
			return err
		}
	}
	return nil
}
