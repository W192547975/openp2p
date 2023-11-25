package message

import "encoding/binary"

type OpenP2PHeader struct {
	DataLen  uint32
	MainType uint16
	SubType  uint16
}

func OpenP2PHeaderSize() int { return binary.Size(OpenP2PHeader{}) }
