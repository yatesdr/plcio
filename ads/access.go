package ads

import (
	"sort"
	"strings"

	"github.com/yatesdr/plcio/metadata"
)

// Access belongs to symbol instances, never the shared datatype cache. Only
// uploaded symbols and validated direct lookups establish known restrictions.
func withinSymbol(root, path string) bool {
	return path == root || (len(path) > len(root) && strings.HasPrefix(path, root) && (path[len(root)] == '.' || path[len(root)] == '['))
}

func (c *Client) readOnlyPaths(name string) ([]string, error) {
	cfg := c.effectiveOptions()
	budget := parseBudget{remaining: uint64(cfg.maxElements), maxDepth: cfg.maxDepth, deadline: c.deadline}
	paths := make(map[string]bool)
	if snapshot := c.snapshot; snapshot != nil {
		names := snapshot.readOnlyNames
		// Ancestors (including an indexed instance) and exact name.
		for i := 0; i <= len(name); i++ {
			if i != len(name) && name[i] != '.' && name[i] != '[' {
				continue
			}
			if err := budget.consume(0); err != nil {
				return nil, err
			}
			prefix := name[:i]
			j := sort.SearchStrings(names, prefix)
			if j < len(names) && names[j] == prefix {
				paths[prefix] = true
			}
		}
		for _, prefix := range []string{name + ".", name + "["} {
			for i := sort.SearchStrings(names, prefix); i < len(names) && strings.HasPrefix(names[i], prefix); i++ {
				if err := budget.consume(1); err != nil {
					return nil, err
				}
				paths[names[i]] = true
			}
		}
	}
	for path, record := range c.lookupSymbols {
		if err := budget.consume(0); err != nil {
			return nil, err
		}
		if !record.info.IsWritable() && (withinSymbol(name, path) || withinSymbol(path, name)) {
			paths[path] = true
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, checkDeadline(c.deadline)
}

func describeAccess(typeOf *metadata.Type, name string, paths []string, budget *parseBudget) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	restrictions := make(map[string]bool, len(paths))
	rootReadOnly := false
	for _, path := range paths {
		if err := budget.consume(0); err != nil {
			return false, err
		}
		if withinSymbol(path, name) {
			rootReadOnly = true
			continue
		}
		// An array describes its element members once. Any restricted element
		// makes that member restricted for a whole-array write. The requested
		// indexed root remains exact; other instances never enter this list.
		relative := strings.TrimPrefix(path, name)
		var normalized strings.Builder
		normalized.Grow(len(relative))
		for i := 0; i < len(relative); i++ {
			if i%128 == 0 {
				if err := budget.consume(0); err != nil {
					return false, err
				}
			}
			if relative[i] == '[' {
				end := strings.IndexByte(relative[i:], ']')
				if end >= 0 {
					i += end
					continue
				}
				normalized.WriteString(relative[i:])
				break
			}
			normalized.WriteByte(relative[i])
		}
		restrictions[normalized.String()] = true
	}
	var annotate func(*metadata.Type, string, bool) error
	annotate = func(current *metadata.Type, path string, inherited bool) error {
		// describeType has already charged every caller-owned node to this
		// budget; annotation adds no expansion and checks the same deadline.
		if err := budget.consume(0); err != nil {
			return err
		}
		inherited = inherited || restrictions[path]
		for i := range current.Members {
			member := &current.Members[i]
			memberPath := path + "." + member.Name
			member.ReadOnly = member.ReadOnly || inherited || restrictions[memberPath]
			if err := annotate(&member.Type, memberPath, member.ReadOnly); err != nil {
				return err
			}
		}
		return nil
	}
	return rootReadOnly, annotate(typeOf, "", rootReadOnly)
}
