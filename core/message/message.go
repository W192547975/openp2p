package message

import "encoding/json"

type Encoder interface{ Encode() ([]byte, error) }
type Decoder interface{ Decode([]byte) error }

type MainTypeMessage interface{ MsgMainType() uint16 }
type MainTypeEncoder interface {
	MainTypeMessage
	Encoder
}
type MainTypeDecoder interface {
	MainTypeMessage
	Size() int
	Decoder
}

type SubTypeMessage interface{ MsgSubType() uint16 }
type RawPacketOwner interface{ RawPacket() interface{} }

func EncodeSubPacket(packet interface{}) ([]byte, error) {
	var r = packet
	for {
		if e, ok := r.(Encoder); ok {
			return e.Encode()
		}
		if o, ok := r.(RawPacketOwner); ok {
			r = o.RawPacket()
			continue
		}
		break
	}
	return json.Marshal(r)
}

type SubTypeChanger struct {
	Type   uint16
	Packet interface{}
}

func (c SubTypeChanger) MsgSubType() uint16 {
	return c.Type
}
func (c SubTypeChanger) RawPacket() interface{} {
	return c.Packet
}

type RawSubData struct {
	Type uint16
	Data []byte
}

func (r RawSubData) MsgSubType() uint16 {
	return r.Type
}
func (r RawSubData) Encode() ([]byte, error) {
	return r.Data, nil
}
