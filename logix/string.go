package logix

import (
	"encoding/binary"
	"fmt"
)

// Recognize the standard STRING by its declared template and layout, never by
// a guessed handle or template ID. Custom structures keep member-map decoding.
func isStringTemplate(tmpl *Template) bool {
	if tmpl == nil || (tmpl.Name != "STRING" && tmpl.Name != "ASCIISTRING82") {
		return false
	}
	length, data := tmpl.GetMember("LEN"), tmpl.GetMember("DATA")
	return length != nil && data != nil && !length.Hidden && !data.Hidden &&
		len(tmpl.MemberMap) == 2 && !length.IsArray() && length.Type == TypeDINT &&
		length.Offset == 0 && data.Type&0x0fff == TypeSINT && !data.IsStructure() &&
		data.Offset == 4 && len(data.ArrayDims) == 1 && data.ArrayDims[0] > 0 &&
		(tmpl.Name != "ASCIISTRING82" || (data.ArrayDims[0] == 82 && tmpl.Size == 88))
}

func decodeTemplateString(tmpl *Template, data []byte, topLevel bool) (string, error) {
	if topLevel {
		if len(data) < 2 {
			return "", fmt.Errorf("STRING response missing structure handle")
		}
		data = data[2:]
	}
	if len(data) < 4 {
		return "", fmt.Errorf("STRING response missing LEN")
	}
	length := int64(int32(binary.LittleEndian.Uint32(data[:4])))
	capacity := int64(tmpl.GetMember("DATA").ArrayDims[0])
	if length < 0 || length > capacity || length > int64(len(data)-4) {
		return "", fmt.Errorf("STRING LEN exceeds its declared or available data")
	}
	return string(data[4 : 4+int(length)]), nil
}
