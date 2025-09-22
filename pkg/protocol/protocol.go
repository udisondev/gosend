package protocol

import (
	"crypto/ed25519"
	"errors"
)

var (
	ErrTooShort = errors.New("too short")
)

type ServerMessageType uint8

const (
	SendSuccess ServerMessageType = iota + 1
	SendError
	NotFound
	Income
)

const (
	ChallangeSize       = 32
	ClientIDSize        = ed25519.PublicKeySize
	RequestIDSize       = 16
	MinPayloadSize      = 1
	MinClientMessageLen = RequestIDSize + RequestIDSize + MinPayloadSize
)

func BuildIncome(o ClientMessage, sender [ClientIDSize]byte) []byte {
	return ServerMessage{
		Type:      Income,
		RequestID: o.RequestID,
		SenderID:  sender,
		Payload:   o.Payload,
	}.Mashal()
}

func EncodeSendSuccessResponse(o ClientMessage) []byte {
	return encodeResponse(o, SendSuccess)
}

func EncodeSendErrorResponse(o ClientMessage) []byte {
	return encodeResponse(o, SendError)
}

func EncodeNotFoundResponse(o ClientMessage) []byte {
	return encodeResponse(o, NotFound)
}

func encodeResponse(o ClientMessage, t ServerMessageType) []byte {
	return ServerMessage{
		Type:      t,
		RequestID: o.RequestID,
	}.Mashal()
}
