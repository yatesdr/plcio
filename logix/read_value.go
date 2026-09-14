package logix

import "fmt"

func (c *Client) decodeStructureArray(tmpl *Template, data []byte, count int) ([]any, error) {
	if len(data) < 2 || tmpl.Size == 0 {
		return nil, fmt.Errorf("structure array missing handle or element size")
	}
	data = data[2:]
	if count <= 1 {
		count = len(data) / int(tmpl.Size)
	}
	if count == 0 || uint64(count)*uint64(tmpl.Size) != uint64(len(data)) {
		return nil, fmt.Errorf("structure array has incomplete element data")
	}
	values := make([]any, 0, count)
	for i := 0; i < count; i++ {
		start := i * int(tmpl.Size)
		item := data[start : start+int(tmpl.Size)]
		var value any
		var err error
		if isStringTemplate(tmpl) {
			value, err = decodeTemplateString(tmpl, item, false)
		} else {
			value, err = c.decodeUDTWithTemplateInternal(tmpl, item, false)
		}
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (c *Client) readTagWithMetadata(name string, count uint16) (*TagValue, error) {
	info, known := c.resolveTagInfo(name)
	isStruct := known && IsStructure(info.TypeCode)
	var expected uint32
	if isStruct {
		size := uint64(c.GetElementSize(info.TypeCode)) * uint64(count)
		if size > 1<<32-1 {
			return nil, fmt.Errorf("read %q: structure size exceeds protocol range", name)
		}
		expected = uint32(size)
	}
	tag, partial, err := c.plc.readTagCountInternal(name, count)
	if isStruct && expected > 0 && c.plc.IsConnected() {
		complete := err == nil && !partial
		if complete {
			actual := len(tag.Bytes)
			if IsCIPStructResponse(tag.DataType) {
				actual -= 2
			}
			complete = actual >= 0 && uint64(actual) == uint64(expected)
		}
		if !complete {
			tag, err = c.plc.readTagFragmentedCount(name, count, expected)
		}
	} else if err == nil && partial {
		tag, err = c.plc.readTagChunked(name, count, tag)
	}
	if err != nil {
		return nil, err
	}
	dataType := tag.DataType
	if known && info.TypeCode != 0 {
		dataType = info.TypeCode
	}
	return &TagValue{Name: name, DataType: dataType, Bytes: tag.Bytes, Count: int(count)}, nil
}
