package ads

import "fmt"

// DecodedTagValue bridges a schema-pinned value and the source-compatible raw
// result. Raw is nonnil for every requested slot, including failures.
type DecodedTagValue struct {
	Raw   *TagValue
	Value any
}

// Read uses the same symbolic request engine as ReadDecoded, retaining raw ADS
// results and the stateless GoValue API for existing low-level callers.
func (c *Client) Read(names ...string) ([]*TagValue, error) {
	raw, _, err := c.readEngine(names, false)
	return raw, err
}

// ReadDecoded automatically resolves supported schemas and decodes locally from
// complete buffers. It returns no successful partial record on a decode failure.
func (c *Client) ReadDecoded(names ...string) ([]*DecodedTagValue, error) {
	raw, values, err := c.readEngine(names, true)
	result := make([]*DecodedTagValue, len(raw))
	for i, v := range raw {
		var value any
		if i < len(values) {
			value = values[i]
		}
		result[i] = &DecodedTagValue{Raw: v, Value: value}
	}
	return result, err
}

func failedRead(names []string, err error) []*TagValue {
	result := make([]*TagValue, len(names))
	for i, name := range names {
		result[i] = &TagValue{Name: name, Error: err}
	}
	return result
}

func (c *Client) readEngine(names []string, decode bool) ([]*TagValue, []any, error) {
	if len(names) == 0 {
		return nil, nil, nil
	}
	_, done, err := c.begin(false)
	if err != nil {
		return failedRead(names, err), nil, err
	}
	defer done()
	cfg := c.effectiveOptions()
	cfg.deadline = c.deadline
	if uint64(len(names)) > uint64(cfg.maxElements) {
		err := fmt.Errorf("read request element limit exceeded")
		return failedRead(names, err), nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		err := c.loadSchemaForIO()
		if err != nil {
			if staleSymbolError(err) && attempt == 0 {
				c.invalidateCaches()
				continue
			}
			return failedRead(names, err), nil, err
		}
		snapshot := c.snapshot
		resolver := c.currentResolver()
		raw, entries, err := c.readValues(names)
		for i, value := range raw {
			if value == nil {
				slotErr := err
				if slotErr == nil {
					slotErr = fmt.Errorf("missing ADS result")
				}
				raw[i] = &TagValue{Name: names[i], Error: slotErr}
			}
		}
		stale := staleSymbolError(err)
		for _, value := range raw {
			if staleSymbolError(value.Error) {
				stale = true
			}
		}
		if stale {
			c.invalidateCaches()
			if attempt == 0 {
				continue
			}
			err = &AdsError{Code: ErrDeviceSymbolVersionInvalid}
			for _, value := range raw {
				if value.Error == nil {
					value.Error = err
				}
			}
		}
		var values []any
		if decode {
			values = make([]any, len(raw))
		}
		remaining := uint64(cfg.maxElements)
		for i, value := range raw {
			if entries[i] == nil || value.Error != nil {
				continue
			}
			// A native scalar's raw count is already established by its exact
			// storage width. Aliases/arrays still use the pinned schema below.
			if !decode {
				info := entries[i].Info
				if width := TypeSize(info.TypeCode); width > 0 && uint32(width) == info.Size && info.TypeName == TypeName(info.TypeCode) {
					continue
				}
			}
			schema := c.schemaFor(entries[i].Info, resolver, snapshot)
			if len(schema.dimensions) != 0 {
				count, countErr := countDimensions(schema.dimensions, uint64(cfg.maxElements))
				if countErr == nil {
					value.Count = int(count)
				}
			} else {
				value.Count = 1
			}
			if !decode {
				continue
			}
			cost, costErr := valueExpansion(schema, remaining, 1, cfg.maxDepth, cfg.deadline)
			if costErr != nil {
				value.Error = fmt.Errorf("decode %s: %w", value.Name, costErr)
				continue
			}
			remaining -= cost
			values[i], value.Error = decodeValue(schema, value.Bytes, cfg)
			if value.Error != nil {
				value.Error = fmt.Errorf("decode %s: %w", value.Name, value.Error)
				values[i] = nil
			}
		}
		return raw, values, err
	}
	panic("unreachable")
}
