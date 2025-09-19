package protocol

import (
	"crypto/ed25519"
	"errors"
)

var (
	ErrTooShort = errors.New("too short")
)

const (
	SendSuccess uint32 = 0
	SendError   uint32 = 1
	NotFound    uint32 = 2
	Private     uint32 = 3

	ChallangeSize = 32
	ClientIDSize  = ed25519.PublicKeySize
)

func PrivateMessage(o Outcome, sender [ClientIDSize]byte) []byte {
	income := Income{
		Type:     Private,
		ReqLogID: o.ReqLogID,
		SenderID: sender,
		Payload:  o.Payload,
	}
	b, _ := income.Mashal()
	return b
}

func EncodeSendSuccessResponse(o Outcome) []byte {
	return encodeResponse(o, SendSuccess)
}

func EncodeSendErrorResponse(o Outcome) []byte {
	return encodeResponse(o, SendError)
}

func EncodeNotFoundResponse(o Outcome) []byte {
	return encodeResponse(o, NotFound)
}

func encodeResponse(o Outcome, t uint32) []byte {
	resp := Income{
		Type:     t,
		ReqLogID: o.ReqLogID,
	}

	b, _ := resp.Mashal()
	return b
}
