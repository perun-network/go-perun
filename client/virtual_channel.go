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

package client

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/log"
	"perun.network/go-perun/wallet"
	"perun.network/go-perun/wire"
)

func (c *Client) fundVirtualChannel(ctx context.Context, virtual *Channel, prop *VirtualChannelProposal) (err error) {
	parentID := prop.Parents[virtual.Idx()]
	parent, ok := c.channels.Get(parentID)
	if !ok {
		return errors.New("referenced parent channel not found")
	}

	indexMap := prop.IndexMaps[virtual.Idx()]
	err = parent.proposeVirtualChannelFunding(ctx, virtual, indexMap)
	if err != nil {
		return errors.WithMessage(err, "proposing channel funding")
	}

	return c.completeFunding(ctx, virtual)
}

func (c *Channel) proposeVirtualChannelFunding(ctx context.Context, virtual *Channel, indexMap []channel.Index) (err error) {
	// Lock machine while update is in progress.
	if !c.machMtx.TryLockCtx(ctx) {
		return errors.Errorf("locking machine mutex in time: %v", ctx.Err())
	}
	defer c.machMtx.Unlock()

	state := c.state().Clone()
	state.Version++

	// Deposit initial balances into sub-allocation
	balances := virtual.translateBalances(indexMap)
	state.Allocation.Balances = state.Allocation.Balances.Sub(balances)
	state.AddSubAlloc(*channel.NewSubAlloc(virtual.ID(), balances.Sum(), indexMap))

	err = c.updateGeneric(ctx, state, func(mcu *msgChannelUpdate) wire.Msg {
		return &virtualChannelFundingProposal{
			msgChannelUpdate:       *mcu,
			ChannelParams:          *virtual.Params(),
			InitialState:           *virtual.State(),
			InitialStateSignatures: virtual.machine.CurrentTX().Sigs,
			IndexMap:               indexMap,
		}
	})
	return
}

const responseTimeout = 10 * time.Second              // How long we wait until the proposal response must be transmitted.
const virtualChannelFundingTimeout = 10 * time.Second // How long we wait for a matching funding proposal.

func (c *Client) handleVirtualChannelFundingProposal(
	ch *Channel,
	prop *virtualChannelFundingProposal,
	responder *UpdateResponder,
) {
	err := c.validateVirtualChannelFundingProposal(ch, prop)
	if err != nil {
		c.rejectProposal(responder, err.Error())
	}

	ctx, cancel := context.WithTimeout(c.Ctx(), virtualChannelFundingTimeout)
	defer cancel()

	err = c.fundingWatcher.Await(ctx, prop)
	if err != nil {
		c.rejectProposal(responder, err.Error())
	}

	c.acceptProposal(responder)
}

type dummyAccount struct {
	address wallet.Address
}

func (a *dummyAccount) Address() wallet.Address {
	return a.address
}

func (a *dummyAccount) SignData([]byte) ([]byte, error) {
	panic("dummy")
}

const hubIndex = 0 // The hub's index in a virtual channel machine.

func (c *Client) persistVirtualChannel(ctx context.Context, peers []wire.Address, params channel.Params, state channel.State, sigs []wallet.Sig) (ch *Channel, err error) {
	cID := params.ID()
	ch, err = c.Channel(cID)
	if err == nil {
		err = errors.New("channel already exists")
		return
	}

	ch, err = c.newChannel(&dummyAccount{params.Parts[hubIndex]}, nil, peers, params)
	if err != nil {
		return
	}

	err = ch.init(ctx, &state.Allocation, state.Data)
	if err != nil {
		return
	}

	for i, sig := range sigs {
		err = ch.machine.AddSig(ctx, channel.Index(i), sig)
		if err != nil {
			return
		}
	}

	err = ch.machine.EnableInit(ctx)
	if err != nil {
		return
	}

	err = ch.machine.SetFunded(ctx)
	if err != nil {
		return
	}

	if err := c.pr.ChannelCreated(ctx, ch.machine, peers, nil); err != nil {
		return ch, errors.WithMessage(err, "persisting new channel")
	}
	ok := c.channels.Put(cID, ch)
	if !ok {
		log.Warnf("virtual channel already exists: %v", cID)
	}

	return
}

func (c *Client) validateVirtualChannelFundingProposal(
	ch *Channel,
	prop *virtualChannelFundingProposal,
) error {
	switch {
	case prop.ChannelParams.ID() != prop.InitialState.ID:
		return errors.New("state does not match parameters")
	case !prop.ChannelParams.VirtualChannel:
		return errors.New("virtual channel flag not set")
	case len(prop.InitialState.Locked) > 0:
		return errors.New("cannot have locked funds")
	}

	// Validate signatures.
	for i, sig := range prop.InitialStateSignatures {
		ok, err := channel.Verify(
			prop.ChannelParams.Parts[i],
			&prop.ChannelParams,
			&prop.InitialState,
			sig,
		)
		if err != nil {
			return err
		} else if !ok {
			return errors.New("invalid signature")
		}
	}

	// Validate index map.
	if len(prop.ChannelParams.Parts) != len(prop.IndexMap) {
		return errors.New("index map: invalid length")
	}

	// Assert not contained before
	_, containedBefore := ch.state().SubAlloc(prop.ChannelParams.ID())
	if containedBefore {
		return errors.New("virtual channel already allocated")
	}

	// Assert contained after with correct balances
	expected := channel.NewSubAlloc(prop.ChannelParams.ID(), prop.InitialState.Sum(), prop.IndexMap)
	subAlloc, containedAfter := prop.State.SubAlloc(expected.ID)
	if !containedAfter || subAlloc.Equal(expected) != nil {
		return errors.New("invalid allocation")
	}

	// Validate allocation.

	// Assert equal assets.
	if err := channel.AssetsAssertEqual(ch.state().Assets, prop.InitialState.Assets); err != nil {
		return errors.WithMessage(err, "assets do not match")
	}

	// Assert sufficient funds in parent channel.
	virtual := transformBalances(prop.InitialState.Balances, ch.state().NumParts(), subAlloc.IndexMap)
	if err := ch.state().Balances.AssertGreaterOrEqual(virtual); err != nil {
		return errors.WithMessage(err, "insufficient funds")
	}

	return nil
}

func (c *Client) matchFundingProposal(a, b interface{}) (ok bool) {
	// Cast.
	inputs := []interface{}{a, b}
	props := make([]*virtualChannelFundingProposal, len(inputs))
	for i, x := range inputs {
		var prop *virtualChannelFundingProposal
		prop, ok = x.(*virtualChannelFundingProposal)
		if !ok {
			return
		}
		props[i] = prop
	}

	prop0 := props[0]

	// Check initial state.
	for _, prop := range props {
		ok = prop.InitialState.Equal(&prop0.InitialState) == nil
		if !ok {
			return
		}
	}

	channels, err := c.gatherChannels(props...)
	if err != nil {
		return false
	}

	// Check index map.
	indices := make([]bool, len(prop0.IndexMap))
	for i, prop := range props {
		for j, idx := range prop.IndexMap {
			if idx == channels[i].Idx() {
				indices[j] = true
			}
		}
	}
	for _, ok = range indices {
		if !ok {
			return
		}
	}

	// Store state for withdrawal after dispute.
	peers := c.gatherPeers(channels...)
	virtual, err := c.persistVirtualChannel(c.Ctx(), peers, prop0.ChannelParams, prop0.InitialState, prop0.InitialStateSignatures)
	if err != nil {
		return false
	}
	c.channels.Put(virtual.ID(), virtual)
	return
}

func (c *Client) gatherChannels(props ...*virtualChannelFundingProposal) (channels []*Channel, err error) {
	channels = make([]*Channel, len(props))
	for i, prop := range props {
		var ch *Channel
		ch, err = c.Channel(prop.ID())
		if err != nil {
			return
		}
		channels[i] = ch
	}
	return
}

func (c *Client) gatherPeers(channels ...*Channel) (peers []wire.Address) {
	peers = make([]wire.Address, len(channels))
	for i, ch := range channels {
		chPeers := ch.Peers()
		if len(chPeers) != 2 {
			panic("unsupported number of participants")
		}
		peers[i] = chPeers[1-ch.Idx()]
	}
	return
}
