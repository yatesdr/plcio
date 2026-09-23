package ads

import (
	"fmt"
	"strings"

	"github.com/yatesdr/plcio/metadata"
)

// The observed TwinCAT F009 bit-member alias is accepted only when its BIT
// GUID and bit address agree with a fully uploaded, supported parent layout.
// No parent value read or read/modify/write is involved. Names compare
// case-insensitively, as TwinCAT resolves them; the returned path uses the
// published parent and member spelling.
func (c *Client) validateBitLookup(name string, record *symbolRecord, budget *parseBudget) (string, error) {
	info := record.info
	mismatch := func() error {
		return fmt.Errorf("symbol lookup identity mismatch: got %q, want %q", info.Name, name)
	}
	snapshot := c.snapshot
	prefix := len(info.Name) + 1
	if snapshot == nil || info.IndexGroup != 0x4041 || info.TypeCode != TypeBool || info.Size != 1 || info.TypeName != "BIT" || info.Flags&SymFlagTypeGUID == 0 || info.Flags&(SymFlagInterfacePointer|SymFlagReferenceTo) != 0 || record.unsupported != "" || len(name) < prefix || !sameSymbolName(name[:prefix], info.Name+".") {
		return "", mismatch()
	}
	parent := snapshot.symbols[info.Name]
	bitType := snapshot.entries["BIT"]
	if parent == nil || parent.info.IndexGroup != 0x4040 || bitType == nil || bitType.flags&dtGUID == 0 || bitType.flags&dtBitValues == 0 || bitType.size != 1 || bitType.guid != record.guid {
		return "", mismatch()
	}
	schema := c.schemaFor(parent.info, c.currentResolver(), snapshot)
	offset := uint64(parent.info.IndexOffset) * 8
	path := name[prefix:]
	canonical := info.Name
	depth := uint32(1)
	for path != "" {
		if err := budget.consume(1); err != nil {
			return "", err
		}
		if depth > budget.maxDepth || schema.unsupported != "" || len(schema.dimensions) != 0 || schema.kind != metadata.KindStruct {
			return "", mismatch()
		}
		memberName, rest, more := strings.Cut(path, ".")
		if more && rest == "" {
			return "", mismatch()
		}
		// An exact member wins; otherwise exactly one case-insensitive match.
		var selected, folded *schemaMember
		ambiguous := false
		for i := range schema.members {
			if err := budget.consume(1); err != nil {
				return "", err
			}
			if schema.members[i].name == memberName {
				selected = &schema.members[i]
				break
			}
			if sameSymbolName(schema.members[i].name, memberName) {
				ambiguous = folded != nil
				folded = &schema.members[i]
			}
		}
		if selected == nil && !ambiguous {
			selected = folded
		}
		if selected == nil {
			return "", mismatch()
		}
		offset += selected.offsetBits
		schema = selected.typeOf
		if rest == "" && selected.sizeBits != 1 {
			return "", mismatch()
		}
		canonical += "." + selected.name
		path = rest
		depth++
	}
	if schema.unsupported != "" || schema.kind != metadata.KindBool || !schema.bit || len(schema.dimensions) != 0 || uint64(info.IndexOffset) != offset {
		return "", mismatch()
	}
	return canonical, nil
}
