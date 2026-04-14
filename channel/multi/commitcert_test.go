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

package multi //nolint:testpackage // Test needs access to package-local helpers.

import (
	"bytes"
	"context"
	"math/big"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	_ "perun.network/go-perun/backend/sim/channel"
	_ "perun.network/go-perun/backend/sim/wallet"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
	wtest "perun.network/go-perun/wallet/test"
	"perun.network/go-perun/wire/perunio"
	pkgtest "polycry.pt/poly-go/test"
)

func TestCommitCert_RoundTripAndCertifierDelivery(t *testing.T) {
	rng := pkgtest.Prng(t)
	t.Run("nil canonical state", func(t *testing.T) {
		cert := newTestCommitCertNilState(t)
		roundTripAndAssert(t, cert, false)
	})
	t.Run("non-nil canonical state", func(t *testing.T) {
		cert := newTestCommitCertWithState(t, rng)
		roundTripAndAssert(t, cert, true)
	})
}

func TestCommitCert_SignVerifyCoordinatedStateSig(t *testing.T) {
	rng := pkgtest.Prng(t)
	state := newTestCommitCertWithState(t, rng).CanonicalState
	require.NotNil(t, state)

	wallet := wtest.RandomWallet(channel.TestBackendID)
	acc := wallet.NewRandomAccount(rng)
	coordAddr := acc.Address()

	sig, err := SignCoordinatedState(acc, state, channel.TestBackendID)
	require.NoError(t, err)

	ok, err := VerifyCoordinatedStateSig(coordAddr, state, sig)
	require.NoError(t, err)
	require.True(t, ok)
}

func roundTripAndAssert(t *testing.T, cert CommitCert, expectState bool) {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, perunio.Encode(&buf, cert))

	var decoded CommitCert
	require.NoError(t, perunio.Decode(&buf, &decoded))

	require.Equal(t, cert.ChannelID, decoded.ChannelID)
	require.Equal(t, cert.Version, decoded.Version)
	require.Equal(t, cert.CoordSig, decoded.CoordSig)

	if expectState {
		require.NotNil(t, decoded.CanonicalState)
		require.NoError(t, cert.CanonicalState.Equal(decoded.CanonicalState))
	} else {
		require.Nil(t, decoded.CanonicalState)
	}

	mock := &mockCommitCertifier{}
	require.NoError(t, mock.CommitCanonicalState(context.Background(), decoded))
	require.True(t, mock.called)
	require.Equal(t, decoded.ChannelID, mock.last.ChannelID)
	require.Equal(t, decoded.Version, mock.last.Version)
}

func newTestCommitCertNilState(t *testing.T) CommitCert {
	t.Helper()

	chID := channel.ID{1}

	return CommitCert{
		ChannelID:      chID,
		CanonicalState: nil,
		Version:        channel.Version(11),
		CoordSig:       []byte{1, 2, 3, 4},
	}
}

func newTestCommitCertWithState(t *testing.T, rng *rand.Rand) CommitCert {
	t.Helper()

	state := &channel.State{
		ID:      channel.ID{2},
		Version: 22,
		Allocation: *func() *channel.Allocation {
			asset := channel.NewAsset(channel.TestBackendID)
			alloc := channel.NewAllocation(2, []wallet.BackendID{channel.TestBackendID}, asset)
			alloc.Balances = channel.Balances{{big.NewInt(9), big.NewInt(3)}}

			return alloc
		}(),
		App:     channel.NoApp(),
		Data:    channel.NoData(),
		IsFinal: true,
	}

	sig := make([]byte, 64)
	_, _ = rng.Read(sig)

	return CommitCert{
		ChannelID:      state.ID,
		CanonicalState: state,
		Version:        state.Version,
		CoordSig:       sig,
	}
}

type mockCommitCertifier struct {
	called bool
	last   CommitCert
}

func (m *mockCommitCertifier) CommitCanonicalState(_ context.Context, cert CommitCert) error {
	m.called = true
	m.last = cert

	return nil
}
