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
	HeaderLen           = 4 + RequestIDSize + ClientIDSize
	MinClientMessageLen = RequestIDSize + ClientIDSize + MinPayloadSize
)

func BuildIncome(o ClientMessage, sender [ClientIDSize]byte) []byte {
	return ServerMessage{
		Type:      Income,
		RequestID: o.RequestID,
		SenderID:  sender,
		Payload:   o.Payload,
	}.Mashal()
}

func EncodeSendSuccessResponse(reqID [RequestIDSize]byte) []byte {
	return encodeResponse(reqID, SendSuccess)
}

func EncodeSendErrorResponse(reqID [RequestIDSize]byte) []byte {
	return encodeResponse(reqID, SendError)
}

func EncodeNotFoundResponse(reqID [RequestIDSize]byte) []byte {
	return encodeResponse(reqID, NotFound)
}

func encodeResponse(reqID [RequestIDSize]byte, t ServerMessageType) []byte {
	return ServerMessage{
		Type:      t,
		RequestID: reqID,
	}.Mashal()
}
