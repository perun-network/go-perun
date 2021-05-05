// Copyright 2020 - See NOTICE file for copyright holders.
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

	"github.com/stretchr/testify/require"
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

const port uint16 = 5000

func SetupHubs(t *testing.T, alice, bob, ingrid *Client) {
	hubA := client.NewHub("127.0.0.1", port)
	hubB := client.NewHub("127.0.0.1", port)
	hubI := client.NewHub("127.0.0.1", port)

	// Ingrid Listen
	go func() {
		if err := hubI.SetupPassive(2); err != nil {
			panic(err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	// Alice and Bob connect
	require.NoError(t, hubA.SetupActive())
	require.NoError(t, hubB.SetupActive())

	alice.SetHub(hubA)
	bob.SetHub(hubB)
	ingrid.SetHub(hubI)
}

func TestVirtualChannelsOptimistic(t *testing.T) {
	rng := test.Prng(t)
	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	asset := chtest.NewRandomAsset(rng)

	clients := NewClients(
		rng,
		[]string{"Alice", "Bob", "Ingrid"},
		t,
	)
	alice, bob, ingrid := clients[0], clients[1], clients[2]
	SetupHubs(t, alice, bob, ingrid)

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
	initBalsAlice := []*big.Int{big.NewInt(10), big.NewInt(10)}
	initAllocAlice := channel.Allocation{
		Assets:   []channel.Asset{asset},
		Balances: [][]channel.Bal{[]*big.Int{initBalsAlice[0], initBalsAlice[1]}},
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
	initBalsBob := []*big.Int{big.NewInt(10), big.NewInt(10)}
	initAllocBob := channel.Allocation{
		Assets:   []channel.Asset{asset},
		Balances: [][]channel.Bal{[]*big.Int{initBalsBob[0], initBalsBob[1]}},
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
	var chAliceBobBob *client.Channel
	proposalHandlerBob := &FunctionProposalHandler{
		openingProposalHandler: func(cp client.ChannelProposal, pr *client.ProposalResponder) {
			switch cp := cp.(type) {
			case *client.VirtualChannelProposal:
				_chAliceBobBob, err := pr.Accept(ctx, cp.Accept(bob.Identity.Address()))
				if err != nil {
					t.Fatalf("accepting virtual channel proposal: %v", err)
				}
				chAliceBobBob = _chAliceBobBob
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
	initBalsVirtual := []*big.Int{big.NewInt(5), big.NewInt(5)}
	initAllocVirtual := channel.Allocation{
		Assets:   []channel.Asset{asset},
		Balances: [][]channel.Bal{[]*big.Int{initBalsVirtual[0], initBalsVirtual[1]}},
	}
	vcp, err := client.NewVirtualChannelProposal(
		chAliceIngrid.ID(),
		chBobIngrid.ID(),
		challengeDuration,
		alice.Identity.Address(),
		&initAllocVirtual,
		[]wire.Address{alice.Identity.Address(), bob.Identity.Address()},
	)
	if err != nil {
		t.Fatalf("creating virtual channel proposal: %v", err)
	}

	chAliceBobAlice, err := alice.ProposeChannel(ctx, vcp)
	if err != nil {
		t.Fatalf("opening channel between Alice and Bob: %v", err)
	}

	err = chAliceBobAlice.UpdateBy(ctx, func(s *channel.State) error {
		diff := int64(3)
		s.Balances[0][0].Sub(s.Balances[0][0], big.NewInt(diff))
		s.Balances[0][1].Add(s.Balances[0][1], big.NewInt(diff))
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
		errs <- chAliceBobBob.Settle(ctx, false)
	}()

	if err := <-errs; err != nil {
		t.Fatalf("closing virtual channel: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("closing virtual channel: %v", err)
	}
}
