// Package common: shared types và định nghĩa Byte Protocol (Delta, Packet) để module/supervisor nói chuyện.
package common

import (
	"encoding/binary"
	"io"
)

// MessageType: loại dữ liệu (1 byte) — Pipe / FFI
type MessageType byte

const (
	MsgPodStatus  MessageType = 0x01
	MsgNodeMetrics MessageType = 0x02
	MsgConsoleLog  MessageType = 0x03
	MsgUIAction    MessageType = 0x04 // User click từ Flutter
	MsgError       MessageType = 0xFF
)

// Packet: gói tin qua Pipe hoặc FFI. Format: [Type(1b)][PayloadLength(4b BE)][Payload(nb)]
type Packet struct {
	Type    MessageType
	Payload []byte
}

// Encode biến Packet thành bytes để gửi đi.
func (p *Packet) Encode() []byte {
	buf := make([]byte, 5+len(p.Payload))
	buf[0] = byte(p.Type)
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(p.Payload)))
	copy(buf[5:], p.Payload)
	return buf
}

// Decode đọc 5 byte header rồi payload từ r, trả về Packet hoặc lỗi.
func Decode(r io.Reader) (*Packet, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[1:5])
	p := &Packet{Type: MessageType(header[0])}
	if length > 0 {
		p.Payload = make([]byte, length)
		if _, err := io.ReadFull(r, p.Payload); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// --- Delta (DiffStream) ---

// Cấu trúc mỗi Delta (little-endian):
//
//	[Header: 1 byte] [Field_ID: 2 bytes] [Data_Length: 4 bytes] [Actual_Bytes]
//
// Nhiều block nối tiếp = DiffStream.
const (
	HeaderDeltaV1 = 0x01
)

// Field_ID (2 bytes) — đọc để biết update field nào (mở rộng tùy module).
const (
	FieldRAMUsedPercent = 1
	FieldCPUUsedPercent = 2
	FieldRAMUsedMB      = 3
	FieldRAMTotalMB     = 4
)

// DiffStream là frame binary chứa 0 hoặc nhiều block Delta.
type DiffStream []byte
