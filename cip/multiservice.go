package cip

import (
	"encoding/binary"
	"fmt"
)

// Multiple Service Packet (service 0x0A) allows batching multiple CIP requests.
const SvcMultipleServicePacket byte = 0x0A

// MultiServiceRequest represents a single request within a Multiple Service Packet.
type MultiServiceRequest struct {
	Service  byte
	Path     EPath_t
	Data     []byte
}

// BuildMultipleServiceRequest builds a Multiple Service Packet request.
// Each individual request is wrapped and offsets are calculated.
func BuildMultipleServiceRequest(requests []MultiServiceRequest) ([]byte, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("MultipleService: no requests provided")
	}
	if len(requests) > 200 {
		return nil, fmt.Errorf("MultipleService: too many requests (%d), max 200", len(requests))
	}

	// Build each individual request and calculate offsets
	var serviceData [][]byte
	for _, req := range requests {
		// Each service: [service 1] [path size 1] [path n] [data n]
		svcBytes := make([]byte, 0, 2+len(req.Path)+len(req.Data))
		svcBytes = append(svcBytes, req.Service)
		svcBytes = append(svcBytes, req.Path.WordLen())
		svcBytes = append(svcBytes, req.Path...)
		svcBytes = append(svcBytes, req.Data...)
		serviceData = append(serviceData, svcBytes)
	}

	// Calculate total size and offsets
	// Header: [service count: 2 bytes] [offsets: 2 bytes each]
	headerSize := 2 + len(requests)*2

	offsets := make([]uint16, len(requests))
	currentOffset := uint16(headerSize)
	for i, svc := range serviceData {
		offsets[i] = currentOffset
		currentOffset += uint16(len(svc))
	}

	// Build the complete request
	result := make([]byte, 0, int(currentOffset))

	// Service count
	result = binary.LittleEndian.AppendUint16(result, uint16(len(requests)))

	// Offsets
	for _, offset := range offsets {
		result = binary.LittleEndian.AppendUint16(result, offset)
	}

	// Service data
	for _, svc := range serviceData {
		result = append(result, svc...)
	}

	return result, nil
}

// MultiServiceResponse represents a single response from a Multiple Service Packet.
type MultiServiceResponse struct {
	Service       byte   // Reply service code (original | 0x80)
	Status        byte   // General status
	ExtStatus     []byte // Extended status (if any)
	Data          []byte // Response data
}

// ParseMultipleServiceResponse parses a Multiple Service Packet response.
func ParseMultipleServiceResponse(data []byte) ([]MultiServiceResponse, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("MultipleService response too short: %d bytes", len(data))
	}

	serviceCount := binary.LittleEndian.Uint16(data[0:2])
	if serviceCount == 0 {
		return nil, nil
	}

	// Calculate minimum size needed for offsets
	minSize := 2 + int(serviceCount)*2
	if len(data) < minSize {
		return nil, fmt.Errorf("MultipleService response too short for %d services", serviceCount)
	}

	// Read offsets
	offsets := make([]uint16, serviceCount)
	for i := 0; i < int(serviceCount); i++ {
		offsets[i] = binary.LittleEndian.Uint16(data[2+i*2 : 4+i*2])
	}

	// Parse each service response
	responses := make([]MultiServiceResponse, serviceCount)
	for i := 0; i < int(serviceCount); i++ {
		start := int(offsets[i])

		// Determine end of this response
		var end int
		if i < int(serviceCount)-1 {
			end = int(offsets[i+1])
		} else {
			end = len(data)
		}

		if start >= len(data) || start >= end {
			continue
		}

		svcData := data[start:end]
		if len(svcData) < 4 {
			continue
		}

		resp := MultiServiceResponse{
			Service: svcData[0],
			// svcData[1] is reserved
			Status: svcData[2],
		}

		extStatusSize := int(svcData[3]) * 2 // Size in words
		dataStart := 4 + extStatusSize

		if extStatusSize > 0 && len(svcData) >= 4+extStatusSize {
			resp.ExtStatus = svcData[4 : 4+extStatusSize]
		}

		if dataStart < len(svcData) {
			resp.Data = svcData[dataStart:]
		}

		responses[i] = resp
	}

	return responses, nil
}

// MultiServiceError represents an error from one service in a batch.
type MultiServiceError struct {
	Index  int
	Status byte
	Msg    string
}

