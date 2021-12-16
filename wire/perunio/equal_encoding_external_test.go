// Copyright 2019 - See NOTICE file for copyright holders.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package perunio_test

import (
	"io"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"perun.network/go-perun/wire/perunio"

	polytest "polycry.pt/poly-go/test"
)

// TestEqualEncoding tests EqualEncoding.
func TestEqualEncoding(t *testing.T) {
	rng := polytest.Prng(t)
	a := make(ByteSlice, 10)
	b := make(ByteSlice, 10)
	c := make(ByteSlice, 12)

	rng.Read(a)
	rng.Read(b)
	rng.Read(c)
	c2 := c

	tests := []struct {
		a         perunio.Encoder
		b         perunio.Encoder
		shouldOk  bool
		shouldErr bool
		name      string
	}{
		{a, nil, false, true, "one Encoder set to nil"},
		{nil, a, false, true, "one Encoder set to nil"},
		{perunio.Encoder(nil), b, false, true, "one Encoder set to nil"},
		{b, perunio.Encoder(nil), false, true, "one Encoder set to nil"},

		{nil, nil, true, false, "both Encoders set to nil"},
		{perunio.Encoder(nil), perunio.Encoder(nil), true, false, "both Encoders set to nil"},

		{a, a, true, false, "same Encoders"},
		{a, &a, true, false, "same Encoders"},
		{&a, a, true, false, "same Encoders"},
		{&a, &a, true, false, "same Encoders"},

		{c, c2, true, false, "different Encoders and same content"},

		{a, b, false, false, "different Encoders and different content"},
		{a, c, false, false, "different Encoders and different content"},
	}

	for _, tt := range tests {
		ok, err := perunio.EqualEncoding(tt.a, tt.b)

		assert.Equalf(t, ok, tt.shouldOk, "EqualEncoding with %s should return %t as bool but got: %t", tt.name, tt.shouldOk, ok)
		assert.Falsef(t, (err == nil) && tt.shouldErr, "EqualEncoding with %s should return an error but got nil", tt.name)
		assert.Falsef(t, (err != nil) && !tt.shouldErr, "EqualEncoding with %s should return nil as error but got: %s", tt.name, err)
	}
}

// ByteSlice is a serializer byte slice.
type ByteSlice []byte //TODO remove

// Encode writes len(b) bytes to the stream. Note that the length itself is not
// written to the stream.
func (b ByteSlice) Encode(w io.Writer) error {
	_, err := w.Write(b)
	return errors.Wrap(err, "failed to write []byte")
}

// Decode reads a byte slice from the given stream.
// Decode reads exactly len(b) bytes.
// This means the caller has to specify how many bytes he wants to read.
func (b *ByteSlice) Decode(r io.Reader) error {
	// This is almost the same as io.ReadFull, but it also fails on closed
	// readers.
	n, err := r.Read(*b)
	for n < len(*b) && err == nil {
		var nn int
		nn, err = r.Read((*b)[n:])
		n += nn
	}
	return errors.Wrap(err, "failed to read []byte")
}
