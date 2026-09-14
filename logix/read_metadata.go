package logix

import (
	"strconv"
	"strings"
)

// resolveTagInfo derives an exact indexed/member path from the discovered root
// and its templates. Derived paths stay out of the catalog and use symbolic
// addressing; an element must not inherit its container's count or instance.
func (c *Client) resolveTagInfo(name string) (TagInfo, bool) {
	if c == nil {
		return TagInfo{}, false
	}
	if info, ok := c.tagInfo[name]; ok {
		return info, true
	}
	root := rootTagName(name)
	info, ok := c.tagInfo[root]
	if !ok {
		return TagInfo{}, false
	}
	path := name[len(root):]
	for path != "" {
		switch path[0] {
		case '[':
			end := strings.IndexByte(path, ']')
			if end < 0 {
				return TagInfo{}, false
			}
			indices := strings.Split(path[1:end], ",")
			if len(indices) > len(info.Dimensions) {
				return TagInfo{}, false
			}
			for i, text := range indices {
				index, err := strconv.ParseUint(text, 10, 32)
				if err != nil || info.Dimensions[i] <= 0 || index >= uint64(info.Dimensions[i]) {
					return TagInfo{}, false
				}
			}
			info.Dimensions = info.Dimensions[len(indices):]
			// Symbol type bits 13..14 contain the remaining array rank.
			info.TypeCode = info.TypeCode&^uint16(0x6000) | uint16(len(info.Dimensions))<<13
			path = path[end+1:]
		case '.':
			if len(info.Dimensions) != 0 || !IsStructure(info.TypeCode) {
				return TagInfo{}, false
			}
			path = path[1:]
			end := strings.IndexAny(path, ".[")
			if end < 0 {
				end = len(path)
			}
			tmpl, err := c.GetTemplate(info.TypeCode)
			if err != nil {
				return TagInfo{}, false
			}
			member := tmpl.GetMember(path[:end])
			if member == nil || member.Hidden {
				return TagInfo{}, false
			}
			info.TypeCode = member.Type
			info.Dimensions = member.ArrayDims
			path = path[end:]
		default:
			return TagInfo{}, false
		}
	}
	info.Name, info.Instance = name, 0
	info.Dimensions = append([]int(nil), info.Dimensions...)
	return info, true
}

func rootTagName(name string) string {
	start := 0
	if strings.HasPrefix(name, "Program:") {
		if dot := strings.IndexByte(name, '.'); dot >= 0 {
			start = dot + 1
		}
	}
	if end := strings.IndexAny(name[start:], ".["); end >= 0 {
		return name[:start+end]
	}
	return name
}
