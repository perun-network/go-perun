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

package test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/client"
	"perun.network/go-perun/wallet"
	"perun.network/go-perun/wire"
)

// TestMultiLedgerDesyncHighestVersion verifies that settlement fails for
// desynchronized coordinated versions across ledgers and succeeds once both
// ledgers are aligned to the highest version.
func TestMultiLedgerDesyncHighestVersion(
	ctx context.Context,
	t *testing.T,
	mlt MultiLedgerSetup,
	challengeDuration uint64,
) {
	require := require.New(t)
	alice, bob := mlt.Client1, mlt.Client2

	parts := []map[wallet.BackendID]wire.Address{alice.WireAddress, bob.WireAddress}
	initAlloc := channel.NewAllocation(
		len(parts),
		[]wallet.BackendID{
			wallet.BackendID(mlt.Asset1.LedgerBackendID().BackendID()),
			wallet.BackendID(mlt.Asset2.LedgerBackendID().BackendID()),
		},
		mlt.Asset1,
		mlt.Asset2,
	)
	initAlloc.Balances = mlt.InitBalances
	prop, err := client.NewLedgerChannelProposal(
		challengeDuration,
		alice.WalletAddress,
		initAlloc,
		parts,
		client.WithCoordinator(alice.WalletAddress),
	)
	require.NoError(err)

	channels := make(chan *client.Channel, 1)
	errs := make(chan error)
	//nolint:contextcheck // This is a test.
	go alice.Handle(
		AlwaysRejectChannelHandler(ctx, errs),
		AlwaysAcceptUpdateHandler(ctx, errs),
	)
	//nolint:contextcheck // This is a test.
	go bob.Handle(
		AlwaysAcceptChannelHandler(ctx, bob.WalletAddress, channels, errs),
		AlwaysAcceptUpdateHandler(ctx, errs),
	)

	chAliceBob, err := alice.ProposeChannel(ctx, prop)
	require.NoError(err)
	var chBobAlice *client.Channel
	select {
	case chBobAlice = <-channels:
	case err := <-errs:
		t.Fatalf("Error in go-routine: %v", err)
	}

	err = chAliceBob.Update(ctx, func(s *channel.State) {
		s.Balances = mlt.UpdateBalances1
	})
	require.NoError(err)
	err = chAliceBob.Update(ctx, func(s *channel.State) {
		s.IsFinal = true
	})
	require.NoError(err)

	highest := client.NewTestChannel(chAliceBob).AdjudicatorReq().Tx.Version
	require.NotZero(highest)
	stale := highest - 1

	// First attempt: ledgers receive different coordinated versions.
	settleAttempt1 := make(chan error, 1)
	go func() {
		settleAttempt1 <- chAliceBob.Settle(ctx, false)
	}()
	time.Sleep(100 * time.Millisecond) //nolint:mnd // Allow settle to subscribe and wait for coordination.
	mlt.Backend1.NotifyCoordinatedWithVersion(chAliceBob.ID(), stale)
	mlt.Backend2.NotifyCoordinatedWithVersion(chAliceBob.ID(), highest)
	err = <-settleAttempt1
	require.Error(err, "desynchronized coordinated versions must fail settlement")

	// Second attempt: coordinator aligns both ledgers to the highest version.
	settleAttempt2 := make(chan error, 1)
	go func() {
		settleAttempt2 <- chAliceBob.Settle(ctx, false)
	}()
	time.Sleep(100 * time.Millisecond) //nolint:mnd // Allow settle to subscribe and wait for coordination.
	mlt.Backend1.NotifyCoordinatedWithVersion(chAliceBob.ID(), highest)
	mlt.Backend2.NotifyCoordinatedWithVersion(chAliceBob.ID(), highest)
	err = <-settleAttempt2
	require.NoError(err)

	err = chBobAlice.Settle(ctx, false)
	require.NoError(err)
}
