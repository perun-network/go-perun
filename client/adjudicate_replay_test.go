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

package client

import (
	"context"
	"math/big"
	"testing"

	_ "perun.network/go-perun/backend/sim"
	chtest "perun.network/go-perun/channel/test"
	wtest "perun.network/go-perun/wallet/test"
	wiretest "perun.network/go-perun/wire/test"

	"github.com/stretchr/testify/require"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
	"perun.network/go-perun/watcher"
	"perun.network/go-perun/wire"
	pkgtest "polycry.pt/poly-go/test"
)

type replayRecordingWatcher struct {
	initial   channel.SignedState
	statesPub *replayRecordingStatesPub
}

func (w *replayRecordingWatcher) StartWatchingLedgerChannel(_ context.Context, signedState channel.SignedState) (watcher.StatesPub, watcher.AdjudicatorSub, error) {
	w.initial = signedState
	return w.statesPub, replayClosedAdjudicatorSub{}, nil
}

func (*replayRecordingWatcher) StartWatchingSubChannel(context.Context, channel.ID, channel.SignedState) (watcher.StatesPub, watcher.AdjudicatorSub, error) {
	panic("unexpected sub-channel watch in replay test")
}

func (*replayRecordingWatcher) StopWatching(context.Context, channel.ID) error {
	return nil
}

type replayRecordingStatesPub struct {
	published []channel.Transaction
}

func (p *replayRecordingStatesPub) Publish(_ context.Context, tx channel.Transaction) error {
	p.published = append(p.published, tx.Clone())
	return nil
}

type replayClosedAdjudicatorSub struct{}

func (replayClosedAdjudicatorSub) EventStream() <-chan channel.AdjudicatorEvent {
	ch := make(chan channel.AdjudicatorEvent)
	close(ch)
	return ch
}

func (replayClosedAdjudicatorSub) Err() error { return nil }

type replayNoopFunder struct{}

func (replayNoopFunder) Fund(context.Context, channel.FundingReq) error { return nil }

type replayNoopAdjudicator struct{}

func (replayNoopAdjudicator) Register(context.Context, channel.AdjudicatorReq, []channel.SignedState) error {
	return nil
}

func (replayNoopAdjudicator) Withdraw(context.Context, channel.AdjudicatorReq, channel.StateMap) error {
	return nil
}

func (replayNoopAdjudicator) Progress(context.Context, channel.ProgressReq) error { return nil }

func (replayNoopAdjudicator) Subscribe(context.Context, channel.ID) (channel.AdjudicatorSubscription, error) {
	return replayNoopSubscription{}, nil
}

type replayNoopSubscription struct{}

func (replayNoopSubscription) Next() channel.AdjudicatorEvent { return nil }
func (replayNoopSubscription) Err() error                     { return nil }
func (replayNoopSubscription) Close() error                   { return nil }

type replayNoopHandler struct{}

func (replayNoopHandler) HandleAdjudicatorEvent(channel.AdjudicatorEvent) {}

func TestWatch_ReplaysCurrentStateAfterStartWatching(t *testing.T) {
	rng := pkgtest.Prng(t)
	const bID = channel.TestBackendID

	ownWallet := wtest.NewWallet(bID)
	ownAcc := ownWallet.NewRandomAccount(rng)
	peerAcc := wtest.NewRandomAccount(rng, bID)
	ownIdentity := wiretest.NewRandomAccountMap(rng, bID)
	peerIdentity := wiretest.NewRandomAccountMap(rng, bID)

	ownAccounts := map[wallet.BackendID]wallet.Account{bID: ownAcc}
	ownParts := map[wallet.BackendID]wallet.Address{bID: ownAcc.Address()}
	peerParts := map[wallet.BackendID]wallet.Address{bID: peerAcc.Address()}
	peers := []map[wallet.BackendID]wire.Address{
		wire.AddressMapfromAccountMap(ownIdentity),
		wire.AddressMapfromAccountMap(peerIdentity),
	}

	params, err := channel.NewParams(60, []map[wallet.BackendID]wallet.Address{ownParts, peerParts}, channel.NoApp(), big.NewInt(1), true, false, channel.ZeroAux)
	require.NoError(t, err)

	machine, err := channel.NewStateMachine(ownAccounts, *params)
	require.NoError(t, err)

	initAlloc := *channel.NewAllocation(2, []wallet.BackendID{bID}, chtest.NewRandomAsset(rng, bID))
	require.NoError(t, machine.Init(initAlloc, channel.NoData()))

	staging := machine.StagingTX()
	ownSig, err := channel.Sign(ownAcc, staging.State, bID)
	require.NoError(t, err)
	peerSig, err := channel.Sign(peerAcc, staging.State, bID)
	require.NoError(t, err)
	require.NoError(t, machine.AddSig(0, ownSig))
	require.NoError(t, machine.AddSig(1, peerSig))
	require.NoError(t, machine.EnableInit())

	statesPub := &replayRecordingStatesPub{}
	w := &replayRecordingWatcher{statesPub: statesPub}

	cl, err := New(
		wire.AddressMapfromAccountMap(ownIdentity),
		wire.NewLocalBus(),
		replayNoopFunder{},
		replayNoopAdjudicator{},
		map[wallet.BackendID]wallet.Wallet{bID: ownWallet},
		w,
	)
	require.NoError(t, err)

	ch, err := cl.channelFromMachine(machine, nil, peers)
	require.NoError(t, err)

	require.NoError(t, ch.Watch(replayNoopHandler{}))
	require.Len(t, statesPub.published, 1)

	replayed := statesPub.published[0]
	current := ch.machine.CurrentTX()
	require.NotNil(t, replayed.State)
	require.Equal(t, current.ID, replayed.ID)
	require.Equal(t, current.Version, replayed.Version)
	require.Equal(t, current.Version, w.initial.State.Version)
}
