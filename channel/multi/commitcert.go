// Copyright 2026 - See NOTICE file for copyright holders.
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

package multi

import (
	"context"
	"io"

	"github.com/pkg/errors"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
	"perun.network/go-perun/wire/perunio"
)

// CommitCert is the boundary object produced by an external coordinator after
// selecting the canonical state for coordinated settlement.
type CommitCert struct {
	ChannelID      channel.ID
	CanonicalState *channel.State
	Version        channel.Version
	// CoordSig is a backend-native signature on CanonicalState.
	//
	// It follows the same backend dispatch path as any channel state
	// signature: coordinators should produce it via channel.Sign and backends
	// should verify it via channel.Verify (through the coordinator address' backend).
	CoordSig wallet.Sig
}

var _ perunio.Serializer = (*CommitCert)(nil)

// Encode serializes the commit certificate for transport or persistence.
func (c CommitCert) Encode(w io.Writer) error {
	hasState := c.CanonicalState != nil
	err := perunio.Encode(w, c.ChannelID, c.Version, hasState)
	if err != nil {
		return errors.WithMessage(err, "commit cert encode")
	}

	if hasState {
		err = perunio.Encode(w, c.CanonicalState)
		if err != nil {
			return errors.WithMessage(err, "commit cert canonical state encode")
		}
	}

	sigLen := uint32(len(c.CoordSig))
	err = perunio.Encode(w, sigLen)
	if err != nil {
		return errors.WithMessage(err, "commit cert signature length encode")
	}

	return errors.WithMessage(perunio.ByteSlice(c.CoordSig).Encode(w), "commit cert signature encode")
}

// Decode deserializes the commit certificate.
func (c *CommitCert) Decode(r io.Reader) error {
	var hasState bool
	err := perunio.Decode(r, &c.ChannelID, &c.Version, &hasState)
	if err != nil {
		return errors.WithMessage(err, "commit cert decode")
	}

	if hasState {
		c.CanonicalState = new(channel.State)
		err = perunio.Decode(r, c.CanonicalState)
		if err != nil {
			return errors.WithMessage(err, "commit cert canonical state decode")
		}
	} else {
		c.CanonicalState = nil
	}

	var sigLen uint32
	err = perunio.Decode(r, &sigLen)
	if err != nil {
		return errors.WithMessage(err, "commit cert signature length decode")
	}

	c.CoordSig = make(wallet.Sig, sigLen)
	sig := perunio.ByteSlice(c.CoordSig)

	return errors.WithMessage(sig.Decode(r), "commit cert signature decode")
}

// CommitCertifier is implemented by chain backends that support the
// Coordinated phase. The external TTP service calls this after selecting the
// canonical state to trigger the on-chain Coordinated transition.
type CommitCertifier interface {
	// CommitCanonicalState verifies and submits a coordinator commit cert.
	//
	// Signature verification semantics are backend-agnostic: implementations
	// should verify CoordSig against cert.CanonicalState using channel.Verify
	// with the configured coordinator address for the target backend.
	CommitCanonicalState(ctx context.Context, cert CommitCert) error
}

// SignCoordinatedState signs a canonical state for inclusion in CommitCert.
//
// It uses the registered channel backend for bID, i.e., the same state-signing
// semantics as participant signatures.
func SignCoordinatedState(
	acc wallet.Account,
	state *channel.State,
	bID wallet.BackendID,
) (wallet.Sig, error) {
	return channel.Sign(acc, state, bID)
}

// VerifyCoordinatedStateSig verifies a CommitCert coordinator signature against
// the canonical state.
//
// Verification dispatches by coordinator.BackendID() via channel.Verify.
func VerifyCoordinatedStateSig(
	coordinator wallet.Address,
	state *channel.State,
	sig wallet.Sig,
) (bool, error) {
	return channel.Verify(coordinator, state, sig)
}
