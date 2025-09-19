package protocol

import (
	"encoding/binary"
	"encoding/hex"
)

type Outcome struct {
	ReqLogID    []byte
	RecipientID [ClientIDSize]byte
	Payload     []byte
}

func (o Outcome) RecipID() [ClientIDSize]byte {
	return [ClientIDSize]byte(o.RecipientID)
}

func (o Outcome) Mashal() ([]byte, error) {
	out := make([]byte, 8+len(o.ReqLogID)+len(o.RecipientID)+len(o.Payload))
	var tail int
	binary.BigEndian.PutUint32(out, uint32(len(o.ReqLogID)))
	tail += 4
	tail += copy(out[tail:], o.ReqLogID)
	binary.BigEndian.PutUint32(out[tail:], uint32(len(o.RecipientID)))
	tail += 4
	tail += copy(out[tail:], o.RecipientID[:])
	copy(out[tail:], o.Payload)

	return out, nil
}

func (o *Outcome) Unmarshal(b []byte) error {
	if len(b) < 4 {
		return ErrTooShort
	}
	reqloglen := binary.BigEndian.Uint32(b[:4])
	var tail uint32 = 4
	if uint32(len(b))-tail < reqloglen {
		return ErrTooShort
	}
	o.ReqLogID = b[tail : tail+reqloglen]
	tail += reqloglen
	if uint32(len(b))-tail < 4 {
		return ErrTooShort
	}
	reciplen := binary.BigEndian.Uint32(b[tail : tail+4])
	tail += 4
	if uint32(len(b))-tail < reciplen {
		return ErrTooShort
	}
	o.RecipientID = [ClientIDSize]byte(b[tail : tail+reciplen])
	tail += reciplen
	o.Payload = b[tail:]

	return nil
}

func (o Outcome) HexReqLogID() string {
	return hex.EncodeToString(o.ReqLogID)
}
