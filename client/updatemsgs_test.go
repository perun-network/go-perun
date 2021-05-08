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

package client

import (
	"math/rand"
	"testing"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/channel/test"
	pkgtest "perun.network/go-perun/pkg/test"
	"perun.network/go-perun/wallet"
	wallettest "perun.network/go-perun/wallet/test"
	"perun.network/go-perun/wire"
)

func TestChannelUpdateSerialization(t *testing.T) {
	rng := pkgtest.Prng(t)
	for i := 0; i < 4; i++ {
		state := test.NewRandomState(rng)
		sig := newRandomSig(rng)
		m := &msgChannelUpdate{
			ChannelUpdate: ChannelUpdate{
				State:    state,
				ActorIdx: channel.Index(rng.Intn(state.NumParts())),
			},
			Sig: sig,
		}
		wire.TestMsg(t, m)
	}
}

func TestChannelUpdateAccSerialization(t *testing.T) {
	rng := pkgtest.Prng(t)
	for i := 0; i < 4; i++ {
		sig := newRandomSig(rng)
		m := &msgChannelUpdateAcc{
			ChannelID: test.NewRandomChannelID(rng),
			Version:   uint64(rng.Int63()),
			Sig:       sig,
		}
		wire.TestMsg(t, m)
	}
}

func TestChannelUpdateRejSerialization(t *testing.T) {
	rng := pkgtest.Prng(t)
	for i := 0; i < 4; i++ {
		m := &msgChannelUpdateRej{
			ChannelID: test.NewRandomChannelID(rng),
			Version:   uint64(rng.Int63()),
			Reason:    newRandomString(rng, 16, 16),
		}
		wire.TestMsg(t, m)
	}
}

func TestVirtualChannelFundingProposalSerialization(t *testing.T) {
	rng := pkgtest.Prng(t)
	for i := 0; i < 1; i++ {
		m := &virtualChannelFundingProposal{
			msgChannelUpdate: msgChannelUpdate{
				ChannelUpdate: ChannelUpdate{
					State:    test.NewRandomState(rng),
					ActorIdx: 1,
				},
				Sig: make([]byte, 64),
			},
			InitialStateSignatures: nil,
			InitialState:           test.NewRandomState(rng),
			ChannelParams:          test.NewRandomParams(rng),
		}
		wire.TestMsg(t, m)
	}
}

// newRandomSig generates a random account and then returns the signature on
// some random data.
func newRandomSig(rng *rand.Rand) wallet.Sig {
	acc := wallettest.NewRandomAccount(rng)
	data := make([]byte, 8)
	rng.Read(data)
	sig, err := acc.SignData(data)
	if err != nil {
		panic("signing error")
	}
	return sig
}

// newRandomstring returns a random string of length between minLen and
// minLen+maxLenDiff.
func newRandomString(rng *rand.Rand, minLen, maxLenDiff int) string {
	r := make([]byte, minLen+rng.Intn(maxLenDiff))
	rng.Read(r)
	return string(r)
}
