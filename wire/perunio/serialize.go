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

package perunio

import (
	"encoding"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/pkg/errors"
)

var byteOrder = binary.LittleEndian

type (
	// Serializer objects can be serialized into and from streams.
	Serializer interface {
		Encoder
		Decoder
	}

	// An Encoder can encode itself into a stream.
	Encoder interface {
		// Encode writes itself to a stream.
		// If the stream fails, the underlying error is returned.
		Encode(io.Writer) error
	}

	// A Decoder can decode itself from a stream.
	Decoder interface {
		// Decode reads an object from a stream.
		// If the stream fails, the underlying error is returned.
		Decode(io.Reader) error
	}
)

// Encode encodes multiple primitive values into a writer.
// All passed values must be copies, not references.
func Encode(writer io.Writer, values ...interface{}) (err error) { //nolint: cyclop // by design,
	// encode function has many paths. Hence, we accept a higher complexity  here.
	for i, value := range values {
		switch v := value.(type) {
		case bool, int8, uint8, int16, uint16, int32, uint32, int64, uint64:
			err = binary.Write(writer, byteOrder, v)
		case time.Time:
			err = binary.Write(writer, byteOrder, v.UnixNano())
		case *big.Int:
			if v.Sign() == -1 {
				err = errors.New("encoding of negative big.Int not implemented")
				break
			}
			err = encodeBytes(writer, v.Bytes())
		case [32]byte:
			_, err = writer.Write(v[:])
		case []byte:
			err = encodeBytes(writer, v)
		case string:
			err = encodeBytes(writer, []byte(v))
		case encoding.BinaryMarshaler:
			var data []byte
			data, err = v.MarshalBinary()
			if err != nil {
				return errors.WithMessage(err, "marshaling to byte array")
			}

			err = encodeBytes(writer, data)
		case Encoder:
			err = v.Encode(writer)
		default:
			panic(fmt.Sprintf("perunio.Encode(): Invalid type %T", v))
		}

		if err != nil {
			return errors.WithMessagef(err, "failed to encode %dth value of type %T", i, value)
		}
	}

	return nil
}

// Decode decodes multiple primitive values from a reader.
// All passed values must be references, not copies.
func Decode(reader io.Reader, values ...interface{}) (err error) {
	for i, value := range values {
		switch v := value.(type) {
		case *bool, *int8, *uint8, *int16, *uint16, *int32, *uint32, *int64, *uint64:
			err = binary.Read(reader, byteOrder, v)
		case *time.Time:
			var nsec int64
			err = binary.Read(reader, byteOrder, &nsec)
			*v = time.Unix(0, nsec)
		case **big.Int:
			var b []byte
			err = decodeBytes(reader, &b)
			if err != nil {
				err = errors.WithMessage(err, "decoding bytes")
				break
			}
			*v = new(big.Int).SetBytes(b)
		case *[32]byte:
			_, err = io.ReadFull(reader, v[:])
		case *[]byte:
			err = decodeBytes(reader, v)
		case *string:
			// err = decodeString(reader, v)
			var b []byte
			err = decodeBytes(reader, &b)
			*v = string(b)
		case encoding.BinaryUnmarshaler:
			var data []byte
			err = decodeBytes(reader, &data)
			if err != nil {
				return errors.WithMessage(err, "decoding data")
			}

			err = v.UnmarshalBinary(data)
			err = errors.WithMessage(err, "unmarshaling binary data")
		default:
			if dec, ok := value.(Decoder); ok {
				err = dec.Decode(reader)
			} else {
				panic(fmt.Sprintf("perunio.Decode(): Invalid type %T", v))
			}
		}

		if err != nil {
			return errors.WithMessagef(err, "failed to decode %dth value of type %T", i, value)
		}
	}

	return nil
}
