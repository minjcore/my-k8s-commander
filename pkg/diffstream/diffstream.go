// Package diffstream: logic tính toán Delta bytes (chỉ gửi field thay đổi).
package diffstream

import (
	"bytes"
	"encoding/binary"

	"my-k8s-commander/pkg/common"
)

// WriteBlock ghi một block Delta vào buf: [Header][Field_ID][Data_Length][Data].
func WriteBlock(buf *bytes.Buffer, fieldID uint16, data []byte) {
	buf.WriteByte(common.HeaderDeltaV1)
	_ = binary.Write(buf, binary.LittleEndian, fieldID)
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
}

// AppendDelta tạo DiffStream từ nhiều cặp (fieldID, data). Dùng khi biết field nào đổi.
func AppendDelta(blocks []struct {
	FieldID uint16
	Data    []byte
}) common.DiffStream {
	var buf bytes.Buffer
	for _, b := range blocks {
		if len(b.Data) == 0 {
			continue
		}
		WriteBlock(&buf, b.FieldID, b.Data)
	}
	return common.DiffStream(buf.Bytes())
}
