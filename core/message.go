package openp2p

import "encoding/json"

type Encoder interface{ Encode() ([]byte, error) }
type Decoder interface{ Decode([]byte) error }

type SubTypeMessage interface{ MsgSubType() uint16 }
type RawPacketOwner interface{ RawPacket() interface{} }

func EncodeSubPacket(packet interface{}) ([]byte, error) {
	var r = packet
	for {
		if e, ok := r.(Encoder); ok {
			return e.Encode()
		}
		if o, ok := r.(RawPacketOwner); ok {
			r = o.RawPackat()
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

type RawSubTypeData struct {
	Type uint16
	Data []byte
}

func (r RawSubTypeData) MsgSubType() uint16 {
	return r.Type
}
func (r RawSubTypeData) Encode() ([]byte, error) {
	return r.Data, nil
}
