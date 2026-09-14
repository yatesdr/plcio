package logix

import "github.com/yatesdr/plcio/cip"

// Size scalar MSPs for both request paths and replies. TCP connectivity alone
// does not imply a negotiated CIP connection; standard Forward Open can also
// fall back to 504 bytes instead of the Large Forward Open size.
func (c *Client) readBatchSize(names []string) int {
	budget := 480
	if c.plc.cipConn != nil && c.plc.connSize > 2 {
		budget = int(c.plc.connSize) - 2 // Connected sequence number.
	} else if len(c.plc.RoutePath) > 0 {
		budget -= 13 + len(c.plc.RoutePath) // Unconnected Send wrapper and padding.
	}
	request, response := 8, 6 // Service/path, count, and reply header/count.
	n := 0
	for _, name := range names {
		path, err := cip.EPath().Symbol(name).Build()
		if err != nil {
			break // Let the ordinary read report the invalid path.
		}
		size := 8 // Conservative maximum atomic width when metadata is absent.
		if info, ok := c.resolveTagInfo(name); ok {
			if width := TypeSize(BaseType(info.TypeCode)); width > 0 {
				size = width
			}
		}
		nextRequest, nextResponse := request+len(path)+6, response+size+8
		if nextRequest > budget || nextResponse > budget || n == 50 {
			break
		}
		request, response = nextRequest, nextResponse
		n++
	}
	if n == 0 {
		return 1
	}
	return n
}
