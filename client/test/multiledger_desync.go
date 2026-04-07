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

	chAliceBob, _, err := proposeAndFinalizeLedgerChannel(ctx, challengeDuration, mlt, alice, bob, channels, errs)
	require.NoError(err)

	highest := client.NewTestChannel(chAliceBob).AdjudicatorReq().Tx.Version
	require.NotZero(highest)
	stale := highest - 1

	// First attempt: ledgers receive different coordinated versions.
	attemptCtx1, cancel1 := context.WithTimeout(ctx, 2*time.Second)
	defer cancel1()

	settleAttempt1 := make(chan error, 1)

	go func() {
		settleAttempt1 <- chAliceBob.Settle(attemptCtx1, false)
	}()

	require.NoError(waitForRegisteredOrProgressed(attemptCtx1, chAliceBob))
	mlt.Backend1.NotifyCoordinatedWithVersion(chAliceBob.ID(), stale)
	mlt.Backend2.NotifyCoordinatedWithVersion(chAliceBob.ID(), highest)

	err = <-settleAttempt1
	require.Error(err, "desynchronized coordinated versions must fail settlement")

	// Second channel: coordinator aligns both ledgers to the highest version.
	chAliceBob2, chBobAlice2, err := proposeAndFinalizeLedgerChannel(ctx, challengeDuration, mlt, alice, bob, channels, errs)
	require.NoError(err)

	highest2 := client.NewTestChannel(chAliceBob2).AdjudicatorReq().Tx.Version
	require.NotZero(highest2)

	attemptCtx2, cancel2 := context.WithTimeout(ctx, 4*time.Second)
	defer cancel2()

	settleAttempt2 := make(chan error, 1)

	go func() {
		settleAttempt2 <- chAliceBob2.Settle(attemptCtx2, false)
	}()

	require.NoError(waitForRegisteredOrProgressed(attemptCtx2, chAliceBob2))
	mlt.Backend1.NotifyCoordinatedWithVersion(chAliceBob2.ID(), highest2)
	mlt.Backend2.NotifyCoordinatedWithVersion(chAliceBob2.ID(), highest2)

	err = <-settleAttempt2
	require.NoError(err)

	settleBobCtx, cancelBob := context.WithTimeout(ctx, 4*time.Second)
	defer cancelBob()

	settleBobDone := make(chan error, 1)

	go func() {
		settleBobDone <- chBobAlice2.Settle(settleBobCtx, false)
	}()

	require.NoError(waitForRegisteredOrProgressed(settleBobCtx, chBobAlice2))
	mlt.Backend1.NotifyCoordinatedWithVersion(chBobAlice2.ID(), highest2)
	mlt.Backend2.NotifyCoordinatedWithVersion(chBobAlice2.ID(), highest2)

	err = <-settleBobDone
	require.NoError(err)
}

func proposeAndFinalizeLedgerChannel(
	ctx context.Context,
	challengeDuration uint64,
	mlt MultiLedgerSetup,
	alice MultiLedgerClient,
	bob MultiLedgerClient,
	channels <-chan *client.Channel,
	errs <-chan error,
) (*client.Channel, *client.Channel, error) {
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
	if err != nil {
		return nil, nil, err
	}

	chAliceBob, err := alice.ProposeChannel(ctx, prop)
	if err != nil {
		return nil, nil, err
	}

	var chBobAlice *client.Channel
	select {
	case chBobAlice = <-channels:
	case err := <-errs:
		return nil, nil, err
	}

	err = chAliceBob.Update(ctx, func(s *channel.State) {
		s.Balances = mlt.UpdateBalances1
	})
	if err != nil {
		return nil, nil, err
	}

	err = chAliceBob.Update(ctx, func(s *channel.State) {
		s.IsFinal = true
	})
	if err != nil {
		return nil, nil, err
	}

	return chAliceBob, chBobAlice, nil
}

func waitForRegisteredOrProgressed(ctx context.Context, ch *client.Channel) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		phase := ch.Phase()
		if phase == channel.Registered || phase == channel.Progressed || phase == channel.Progressing {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
