package perunio

import (
	"encoding/binary"
	"io"

	"github.com/pkg/errors"
)

const uint16MaxValue = 0xFFFF

func encodeBytes(w io.Writer, v []byte) error {
	l := len(v)
	if l < 0 || l > uint16MaxValue {
		return errors.Errorf("invalid length: got l = %d, expected 0 <= l <= %d", l, uint16MaxValue)
	}

	// Write length.
	err := binary.Write(w, byteOrder, uint16(l))
	if err != nil {
		return errors.WithMessage(err, "writing length of marshalled data")
	}

	// Write value.
	if l > 0 {
		_, err = w.Write(v)
	}
	return err
}

func decodeBytes(r io.Reader, v *[]byte) error {
	// Read l.
	var l uint16
	err := binary.Read(r, byteOrder, &l)
	if err != nil {
		return errors.WithMessage(err, "reading length of binary data")
	}

	// Read value.
	if l > 0 {
		*v = make([]byte, l)
		_, err = io.ReadFull(r, *v)
	}
	return err
}
