package protocol

import (
	"encoding/binary"
	"encoding/hex"
)

type Income struct {
	Type     uint32             `json:"type"`
	ReqLogID []byte             `json:"req_log_id"`
	SenderID [ClientIDSize]byte `json:"sender_id"`
	Payload  []byte             `json:"payload"`
}

type Status string

const (
	StatusUnknown  Status = "Unknown"
	StatusSuccess  Status = "Success"
	StatusError    Status = "Error"
	StatusNotFound Status = "Not found"
)

func (i Income) Mashal() ([]byte, error) {
	out := make([]byte, 8+len(i.ReqLogID)+len(i.SenderID)+len(i.Payload))
	var tail int
	binary.BigEndian.PutUint32(out[:4], i.Type)
	tail += 4
	binary.BigEndian.PutUint32(out[tail:], uint32(len(i.ReqLogID)))
	tail += 4
	tail += copy(out[tail:], i.ReqLogID)
	tail += copy(out[tail:], i.SenderID[:])
	copy(out[tail:], i.Payload)

	return out, nil
}

func (i *Income) Unmarshal(b []byte) error {
	if len(b) < 8 {
		return ErrTooShort
	}
	i.Type = binary.BigEndian.Uint32(b[:4])
	var tail uint32 = 4
	rl := binary.BigEndian.Uint32(b[tail : tail+4])
	tail += 4
	if uint32(len(b)) < tail+rl {
		return ErrTooShort
	}
	i.ReqLogID = b[tail : tail+rl]
	tail += rl
	if uint32(len(b)) < tail+4 {
		return ErrTooShort
	}
	i.SenderID = [ClientIDSize]byte(b[tail : tail+ClientIDSize])
	tail += ClientIDSize
	i.Payload = b[tail:]

	return nil
}

func (i Income) HexReqLogID() string {
	return hex.EncodeToString(i.ReqLogID)
}

func (i Income) Status() Status {
	switch i.Type {
	case SendSuccess:
		return StatusSuccess
	case SendError:
		return StatusError
	case NotFound:
		return StatusNotFound
	default:
		return StatusUnknown
	}
}
