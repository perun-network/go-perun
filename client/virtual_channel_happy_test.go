// Copyright 2021 - See NOTICE file for copyright holders.
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

package client_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"perun.network/go-perun/channel"
	chtest "perun.network/go-perun/channel/test"
	"perun.network/go-perun/client"
	"perun.network/go-perun/pkg/test"
	"perun.network/go-perun/wire"
)

const challengeDuration = 10
const testDuration = 10 * time.Second

type FunctionProposalHandler struct {
	openingProposalHandler client.ProposalHandlerFunc
	updateProposalHandler  client.UpdateHandlerFunc
}

func (h *FunctionProposalHandler) HandleProposal(p client.ChannelProposal, r *client.ProposalResponder) {
	h.openingProposalHandler(p, r)
}

func (h *FunctionProposalHandler) HandleUpdate(prev *channel.State, next client.ChannelUpdate, r *client.UpdateResponder) {
	h.updateProposalHandler(prev, next, r)
}

func TestVirtualChannelsOptimistic(t *testing.T) {
	rng := test.Prng(t)
	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	// Set test values.
	asset := chtest.NewRandomAsset(rng)
	initBalsAlice := []*big.Int{big.NewInt(10), big.NewInt(10)}    // with Ingrid
	initBalsBob := []*big.Int{big.NewInt(10), big.NewInt(10)}      // with Ingrid
	initBalsVirtual := []*big.Int{big.NewInt(5), big.NewInt(5)}    // Alice proposes
	virtualBalsUpdated := []*big.Int{big.NewInt(2), big.NewInt(8)} // Send 3.
	finalBalsAlice := []*big.Int{big.NewInt(7), big.NewInt(13)}
	finalBalsBob := []*big.Int{big.NewInt(13), big.NewInt(7)}

	// Setup clients.
	clients := NewClients(
		rng,
		[]string{"Alice", "Bob", "Ingrid"},
		t,
	)
	alice, bob, ingrid := clients[0], clients[1], clients[2]

	proposalHandlerIngrid := &FunctionProposalHandler{
		openingProposalHandler: func(cp client.ChannelProposal, pr *client.ProposalResponder) {
			switch cp := cp.(type) {
			case *client.LedgerChannelProposal:
				_, err := pr.Accept(ctx, cp.Accept(ingrid.Identity.Address(), client.WithRandomNonce()))
				if err != nil {
					t.Fatalf("accepting ledger channel proposal: %v", err)
				}
			default:
				t.Fatalf("invalid channel proposal: %v", cp)
			}
		},
	}
	go ingrid.Client.Handle(proposalHandlerIngrid, proposalHandlerIngrid)

	// Establish ledger channel between Alice and Ingrid.
	initAllocAlice := channel.Allocation{
		Assets:   []channel.Asset{asset},
		Balances: [][]channel.Bal{initBalsAlice},
	}
	lcpAlice, err := client.NewLedgerChannelProposal(
		challengeDuration,
		alice.Identity.Address(),
		&initAllocAlice,
		[]wire.Address{alice.Identity.Address(), ingrid.Identity.Address()},
	)
	if err != nil {
		t.Fatalf("creating ledger channel proposal: %v", err)
	}

	chAliceIngrid, err := alice.ProposeChannel(ctx, lcpAlice)
	if err != nil {
		t.Fatalf("opening channel between Alice and Ingrid: %v", err)
	}

	// Establish ledger channel between Bob and Ingrid.
	initAllocBob := channel.Allocation{
		Assets:   []channel.Asset{asset},
		Balances: [][]channel.Bal{initBalsBob},
	}
	lcpBob, err := client.NewLedgerChannelProposal(
		challengeDuration,
		bob.Identity.Address(),
		&initAllocBob,
		[]wire.Address{bob.Identity.Address(), ingrid.Identity.Address()},
	)
	if err != nil {
		t.Fatalf("creating ledger channel proposal: %v", err)
	}

	chBobIngrid, err := bob.ProposeChannel(ctx, lcpBob)
	if err != nil {
		t.Fatalf("opening channel between Bob and Ingrid: %v", err)
	}

	// Setup Bob's proposal handler.
	chAliceBobBob := make(chan *client.Channel, 1)
	proposalHandlerBob := &FunctionProposalHandler{
		openingProposalHandler: func(cp client.ChannelProposal, pr *client.ProposalResponder) {
			switch cp := cp.(type) {
			case *client.VirtualChannelProposal:
				_chAliceBobBob, err := pr.Accept(ctx, cp.Accept(bob.Identity.Address()))
				if err != nil {
					t.Fatalf("accepting virtual channel proposal: %v", err)
				}
				chAliceBobBob <- _chAliceBobBob
			default:
				t.Fatalf("invalid channel proposal: %v", cp)
			}
		},
		updateProposalHandler: func(s *channel.State, cu client.ChannelUpdate, ur *client.UpdateResponder) {
			ur.Accept(ctx)
		},
	}
	go bob.Client.Handle(proposalHandlerBob, proposalHandlerBob)

	// Establish virtual channel between Alice and Bob via Ingrid.
	initAllocVirtual := channel.Allocation{
		Assets:   []channel.Asset{asset},
		Balances: [][]channel.Bal{initBalsVirtual},
	}
	indexMapAlice := []channel.Index{0, 1}
	indexMapBob := []channel.Index{1, 0}
	vcp, err := client.NewVirtualChannelProposal(
		challengeDuration,
		alice.Identity.Address(),
		&initAllocVirtual,
		[]wire.Address{alice.Identity.Address(), bob.Identity.Address()},
		[]channel.ID{chAliceIngrid.ID(), chBobIngrid.ID()},
		[][]channel.Index{indexMapAlice, indexMapBob},
	)
	if err != nil {
		t.Fatalf("creating virtual channel proposal: %v", err)
	}

	chAliceBobAlice, err := alice.ProposeChannel(ctx, vcp)
	if err != nil {
		t.Fatalf("opening channel between Alice and Bob: %v", err)
	}

	err = chAliceBobAlice.UpdateBy(ctx, func(s *channel.State) error {
		s.Balances = channel.Balances{virtualBalsUpdated}
		s.IsFinal = true
		return nil
	})
	if err != nil {
		t.Fatalf("updating virtual channel: %v", err)
	}

	errs := make(chan error, 2)
	go func() {
		errs <- chAliceBobAlice.Settle(ctx, false)
	}()

	go func() {
		ch := <-chAliceBobBob
		errs <- ch.Settle(ctx, false)
	}()

	if err := <-errs; err != nil {
		t.Fatalf("closing virtual channel: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("closing virtual channel: %v", err)
	}

	// Test final balances.
	err = chAliceIngrid.State().Balances.AssertEqual(channel.Balances{finalBalsAlice})
	if err != nil {
		t.Errorf("Alice: invalid final balances: %v", err)
	}
	err = chBobIngrid.State().Balances.AssertEqual(channel.Balances{finalBalsBob})
	if err != nil {
		t.Errorf("Bob: invalid final balances: %v", err)
	}
}
