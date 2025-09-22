package protocol

type ClientMessage struct {
	RequestID   [RequestIDSize]byte
	RecipientID [ClientIDSize]byte
	Payload     []byte
}

func (o ClientMessage) RecipID() [ClientIDSize]byte {
	return [ClientIDSize]byte(o.RecipientID)
}

func (o ClientMessage) Mashal() []byte {
	out := make([]byte, RequestIDSize+ClientIDSize+len(o.Payload))
	tail := copy(out, o.RequestID[:])
	tail = copy(out[tail:], o.RecipientID[:])
	copy(out[tail:], o.Payload)

	return out
}

func (o *ClientMessage) Unmarshal(b []byte) error {
	if len(b) < ClientIDSize+RequestIDSize+1 {
		return ErrTooShort
	}

	var offset int
	o.RequestID = [RequestIDSize]byte(b[:RequestIDSize])
	offset += RequestIDSize

	o.RecipientID = [ClientIDSize]byte(b[offset : offset+ClientIDSize])
	offset += ClientIDSize

	o.Payload = b[offset:]

	return nil
}
