package wire

import (
	"io"
)

type Encoder interface {
	Encode(io.Writer, *Envelope) error
	Decode(io.Reader) (*Envelope, error)
}

var encoder Encoder

func SetEncoder(e Encoder) {
	if encoder != nil {
		panic("encoder already set")
	}
	encoder = e
}

func EncodeEnvelope(w io.Writer, e *Envelope) error {
	return encoder.Encode(w, e)
}

func DecodeEnvelope(r io.Reader) (*Envelope, error) {
	return encoder.Decode(r)
}
