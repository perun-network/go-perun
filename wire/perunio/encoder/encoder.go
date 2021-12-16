package encoder

import (
	"io"

	"perun.network/go-perun/wire"
	"perun.network/go-perun/wire/perunio"
)

type EnvelopeEncoder struct{}

var encoder wire.Encoder = &EnvelopeEncoder{}

func (*EnvelopeEncoder) Encode(w io.Writer, e *wire.Envelope) error {
	return perunio.Encode(w, e)
}

func (*EnvelopeEncoder) Decode(r io.Reader) (*wire.Envelope, error) {
	var e wire.Envelope
	err := perunio.Decode(r, &e)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func init() {
	wire.SetEncoder(encoder)
}
