package protocol

import (
	"errors"
	"fmt"
)

type ServerMessage struct {
	Type      ServerMessageType   `json:"type"`
	RequestID [RequestIDSize]byte `json:"req_log_id"`
	SenderID  [ClientIDSize]byte  `json:"sender_id"`
	Payload   []byte              `json:"payload"`
}

type Status string

const (
	StatusUnknown  Status = "Unknown"
	StatusSuccess  Status = "Success"
	StatusError    Status = "Error"
	StatusNotFound Status = "Not found"
)

func (i ServerMessage) Mashal() []byte {
	mlen := 1 + RequestIDSize
	if i.Type == Income {
		mlen += ClientIDSize + len(i.Payload)
	}
	out := make([]byte, mlen)
	out[0] = byte(i.Type)
	tail := copy(out[1:], i.RequestID[:])

	if i.Type == Income {
		tail = copy(out[tail:], i.SenderID[:])
		copy(out[tail:], i.Payload)
	}

	return out
}

func (i *ServerMessage) Unmarshal(b []byte) error {
	if len(b) < 1+RequestIDSize {
		return ErrTooShort
	}
	t, err := ParseServerMessageType(b[0])
	if err != nil {
		return fmt.Errorf("ParseServerMessageType: %w", err)
	}
	i.Type = t
	if t == Income && len(b) < 1+RequestIDSize+ClientIDSize {
		return ErrTooShort
	}

	tail := 1
	i.RequestID = [RequestIDSize]byte(b[tail : tail+RequestIDSize])
	tail += RequestIDSize

	if t == Income {
		i.SenderID = [ClientIDSize]byte(b[tail : tail+ClientIDSize])
		tail += ClientIDSize
		i.Payload = b[tail:]
	}

	return nil
}

func ParseServerMessageType(b byte) (ServerMessageType, error) {
	switch b {
	case 1:
		return SendSuccess, nil
	case 2:
		return SendSuccess, nil
	case 3:
		return SendSuccess, nil
	case 4:
		return SendSuccess, nil
	default:
		return 0, errors.ErrUnsupported
	}
}

func (i ServerMessage) Status() Status {
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

func (i ServerMessage) IsSuccess() bool {
	return i.Status() == StatusSuccess
}
