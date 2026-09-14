package ads

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/yatesdr/plcio/internal/adsbridge"
)

func init() {
	adsbridge.Catalog = func(client any) ([]adsbridge.Tag, time.Time, error) {
		c, ok := client.(*Client)
		if !ok || c == nil {
			return nil, time.Time{}, fmt.Errorf("invalid ADS catalog client")
		}
		_, done, err := c.begin(false)
		if err != nil {
			return nil, time.Time{}, err
		}
		defer done()
		_, tags, err := c.catalogResult(true)
		return tags, c.deadline, err
	}
}

// Both catalog views pin one snapshot under one gate and operation deadline.
// A changed version discards the entire projection and retries once.
func (c *Client) catalogResult(project bool) ([]TagInfo, []adsbridge.Tag, error) {
	for attempt := 0; attempt < 2; attempt++ {
		err := c.loadSchema()
		var raw []TagInfo
		var projected []adsbridge.Tag
		if err == nil {
			snapshot := c.snapshot
			if project {
				snapshot.resolver.deadline = c.deadline
				projected = make([]adsbridge.Tag, len(snapshot.catalog))
				for i, tag := range snapshot.catalog {
					if err = checkDeadline(c.deadline); err != nil {
						break
					}
					schema := snapshot.resolver.symbol(tag)
					t := adsbridge.Tag{Name: tag.Name, TypeCode: tag.TypeCode, TypeName: tag.TypeName, Writable: tag.IsWritable()}
					for _, dimension := range schema.dimensions {
						t.Dimensions = append(t.Dimensions, dimension.Length)
					}
					projected[i] = t
				}
			} else {
				raw = append([]TagInfo{}, snapshot.catalog...)
			}
			if err == nil {
				_, err = c.checkVersion()
				if err == nil && c.snapshot != snapshot {
					err = &AdsError{Code: ErrDeviceSymbolVersionInvalid}
				}
			}
		}
		if err == nil {
			return raw, projected, nil
		}
		if !staleSymbolError(err) || attempt == 1 {
			return nil, nil, err
		}
		c.invalidateCaches()
	}
	panic("unreachable")
}

func (c *Client) effectiveOptions() options {
	cfg := c.cfg
	defaults := defaultOptions()
	if cfg.maxPayload == 0 {
		cfg.maxPayload = defaults.maxPayload
	}
	if cfg.maxBatch == 0 {
		cfg.maxBatch = defaults.maxBatch
	}
	if cfg.maxMetadata == 0 {
		cfg.maxMetadata = defaults.maxMetadata
	}
	if cfg.maxSymbols == 0 {
		cfg.maxSymbols = defaults.maxSymbols
	}
	if cfg.maxTypes == 0 {
		cfg.maxTypes = defaults.maxTypes
	}
	if cfg.maxDepth == 0 {
		cfg.maxDepth = defaults.maxDepth
	}
	if cfg.maxElements == 0 {
		cfg.maxElements = defaults.maxElements
	}
	return cfg
}

func unsupportedService(err error) bool {
	var device *AdsError
	return errors.As(err, &device) && (device.Code == ErrDeviceSrvNotSupp || device.Code == ErrDeviceInvalidGrp)
}

func staleSymbolError(err error) bool {
	if err == nil {
		return false
	}
	var device *AdsError
	return errors.As(err, &device) && (device.Code == ErrDeviceSymbolVersionInvalid || device.Code == ErrDeviceNotifyHndInvalid || device.Code == ErrDeviceSymbolNotActive)
}

// Only a documented missing catalog permits primitive fallback. Version and
// transport failures still fail the operation when that capability is cached.
func (c *Client) loadSchemaForIO() error {
	if c.catalogUnavailable {
		_, err := c.checkVersion()
		return err
	}
	err := c.loadSchema()
	if c.catalogUnavailable && unsupportedService(err) {
		return nil
	}
	return err
}

func (c *Client) currentResolver() *typeResolver {
	if c.snapshot != nil {
		c.snapshot.resolver.deadline = c.deadline
		return c.snapshot.resolver
	}
	if c.fallbackResolver == nil {
		c.fallbackResolver = newResolver(nil, c.effectiveOptions())
	}
	c.fallbackResolver.deadline = c.deadline
	return c.fallbackResolver
}

// checkVersion invalidates handles, catalog and schema together. Only explicit
// service-not-supported/invalid-group codes are cached as capability limitations.
func (c *Client) checkVersion() (uint32, error) {
	if c.versionCapability == 2 {
		return 0, nil
	}
	data, err := c.readData(IndexGroupSymbolVersion, 0, 4)
	if err != nil {
		if unsupportedService(err) {
			if c.versionSet {
				c.invalidateCaches()
			}
			c.versionCapability = 2
			return 0, nil
		}
		return 0, err
	}
	var version uint32
	switch len(data) {
	case 1:
		version = uint32(data[0])
	case 4:
		version = binary.LittleEndian.Uint32(data)
	default:
		return 0, c.conn.fail(fmt.Errorf("symbol-version payload size %d", len(data)))
	}
	if c.versionSet && c.symbolVersion != version {
		c.invalidateCaches()
	}
	c.symbolVersion, c.versionSet, c.versionCapability = version, true, 1
	return version, nil
}

// loadSchema publishes only a completely parsed upload, bracketed by a supported
// version service. Its caller owns the one recovery attempt for the operation.
func (c *Client) loadSchema() error {
	before, err := c.checkVersion()
	if err != nil {
		return err
	}
	if c.snapshot != nil {
		return nil
	}
	if c.catalogUnavailable {
		return &AdsError{Code: ErrDeviceSrvNotSupp}
	}
	cfg := c.effectiveOptions()
	cfg.deadline = c.deadline
	info, err := c.readData(IndexGroupSymbolUploadInfo2, 0, 24)
	legacy := false
	if unsupportedService(err) {
		legacy = true
		info, err = c.readData(IndexGroupSymbolUploadInfo, 0, 8)
	}
	if err != nil {
		if unsupportedService(err) {
			c.catalogUnavailable = true
		}
		return err
	}
	expected := 24
	if legacy {
		expected = 8
	}
	if len(info) != expected {
		return fmt.Errorf("upload info size %d, want %d", len(info), expected)
	}
	symbolCount, symbolBytes := binary.LittleEndian.Uint32(info[:4]), binary.LittleEndian.Uint32(info[4:8])
	var typeCount, typeBytes uint32
	if !legacy {
		typeCount, typeBytes = binary.LittleEndian.Uint32(info[8:12]), binary.LittleEndian.Uint32(info[12:16])
	}
	if symbolCount > cfg.maxSymbols || typeCount > cfg.maxTypes || uint64(symbolBytes)+uint64(typeBytes) > uint64(cfg.maxMetadata) || uint64(symbolCount)*33 > uint64(symbolBytes) || uint64(typeCount)*45 > uint64(typeBytes) {
		return fmt.Errorf("metadata upload count/aggregate byte limit or inconsistent sizes")
	}
	var symbols, types []byte
	if symbolBytes != 0 {
		symbols, err = c.readData(IndexGroupSymbolUpload, 0, symbolBytes)
		if err != nil {
			return err
		}
	}
	if uint64(len(symbols)) != uint64(symbolBytes) {
		return fmt.Errorf("symbol upload size mismatch")
	}
	tags, records, err := parseSymbolRecords(symbols, symbolCount, cfg)
	if err != nil {
		return err
	}
	entries := make(map[string]*typeEntry)
	if !legacy && !c.typesUnavailable && typeBytes != 0 {
		types, err = c.readData(IndexGroupDataTypeUpload, 0, typeBytes)
		if unsupportedService(err) {
			c.typesUnavailable = true
		} else if err != nil {
			return err
		} else {
			if uint64(len(types)) != uint64(typeBytes) {
				return fmt.Errorf("datatype upload size mismatch")
			}
			entries, err = parseTypeTable(types, typeCount, cfg)
			if err != nil {
				return err
			}
		}
	}
	after, err := c.checkVersion()
	if err != nil {
		return err
	}
	if c.versionCapability == 1 && before != after {
		return &AdsError{Code: ErrDeviceSymbolVersionInvalid}
	}
	snapshot := &schemaSnapshot{bytes: uint64(len(symbols)) + uint64(len(types)), generation: c.generation, version: after, versionKnown: c.versionCapability == 1, catalog: tags, entries: entries, resolver: newResolver(entries, cfg), symbols: records}
	snapshot.resolver.deadline = c.deadline
	if snapshot.bytes+c.lookupBytes > uint64(cfg.maxMetadata) {
		return fmt.Errorf("catalog and lookup aggregate metadata limit exceeded")
	}
	// Validate resolution budgets before publication. Unsupported valid layouts
	// remain advertised and cannot poison unrelated supported symbols.
	for _, tag := range tags {
		if err := checkDeadline(c.deadline); err != nil {
			return err
		}
		snapshot.resolver.symbol(tag)
		if !tag.IsWritable() {
			snapshot.readOnlyNames = append(snapshot.readOnlyNames, tag.Name)
		}
	}
	if snapshot.resolver.expanded > uint64(cfg.maxElements) {
		return fmt.Errorf("catalog schema expansion limit exceeded")
	}
	if err := checkDeadline(c.deadline); err != nil {
		return err
	}
	c.snapshot, c.catalog, c.symbolsLoaded = snapshot, tags, true
	for _, tag := range tags {
		if c.symbols[tag.Name] == nil {
			c.symbols[tag.Name] = &SymbolEntry{Info: tag}
		}
	}
	return nil
}
