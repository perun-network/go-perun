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

	"github.com/pkg/errors"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wire"
)

// withdrawVirtualChannel proposes to release the funds allocated to the
// specified virtual channel.
func (c *Channel) withdrawVirtualChannel(ctx context.Context, virtual *Channel) (err error) {
	// Lock machine while update is in progress.
	if !c.machMtx.TryLockCtx(ctx) {
		return errors.Errorf("locking machine mutex in time: %v", ctx.Err())
	}
	defer c.machMtx.Unlock()

	state := c.state().Clone()
	state.Version++

	virtualAlloc, ok := state.SubAlloc(virtual.ID())
	if !ok {
		c.Log().Panicf("sub-allocation %x not found", virtualAlloc.ID)
	}

	if !virtualAlloc.BalancesEqual(virtual.state().Allocation.Sum()) {
		c.Log().Panic("sub-allocation does not equal accumulated sub-channel outcome")
	}

	virtualBalsRemapped := virtual.translateBalances(virtualAlloc.IndexMap)

	// We assume that the asset types of parent channel and virtual channel are the same.
	state.Allocation.Balances = state.Allocation.Balances.Add(virtualBalsRemapped)

	if err := state.Allocation.RemoveSubAlloc(virtualAlloc); err != nil {
		c.Log().WithError(err).Panicf("removing sub-allocation with id %x", virtualAlloc.ID)
	}

	err = c.updateGeneric(ctx, state, func(mcu *msgChannelUpdate) wire.Msg {
		return &virtualChannelSettlementProposal{
			msgChannelUpdate:     *mcu,
			ChannelParams:        *virtual.Params(),
			FinalState:           *virtual.state(),
			FinalStateSignatures: virtual.machine.CurrentTX().Sigs,
		}
	})

	return errors.WithMessage(err, "update parent channel")
}

func (c *Client) handleVirtualChannelSettlementProposal(
	parent *Channel,
	prop *virtualChannelSettlementProposal,
	responder *UpdateResponder,
) {
	err := c.validateVirtualChannelSettlementProposal(parent, prop)
	if err != nil {
		c.rejectProposal(responder, err.Error())
	}

	ctx, cancel := context.WithTimeout(c.Ctx(), virtualChannelFundingTimeout)
	defer cancel()

	err = c.settlementWatcher.Await(ctx, prop)
	if err != nil {
		c.rejectProposal(responder, err.Error())
	}

	c.acceptProposal(responder)
}

func (c *Client) validateVirtualChannelSettlementProposal(
	parent *Channel,
	prop *virtualChannelSettlementProposal,
) error {
	// Validate parameters.
	if prop.ChannelParams.ID() != prop.FinalState.ID {
		return errors.New("invalid parameters")
	}

	// Validate signatures.
	for i, sig := range prop.FinalStateSignatures {
		ok, err := channel.Verify(
			prop.ChannelParams.Parts[i],
			&prop.ChannelParams,
			&prop.FinalState,
			sig,
		)
		if err != nil {
			return err
		} else if !ok {
			return errors.New("invalid signature")
		}
	}

	// Validate allocation.

	// Assert equal assets.
	if err := channel.AssetsAssertEqual(parent.state().Assets, prop.FinalState.Assets); err != nil {
		return errors.WithMessage(err, "assets do not match")
	}

	// Assert contained before and matching funds
	subAlloc, containedBefore := parent.state().SubAlloc(prop.ChannelParams.ID())
	if !containedBefore || !subAlloc.BalancesEqual(prop.FinalState.Sum()) {
		return errors.New("virtual channel not allocated")
	}

	// Assert not contained after
	_, containedAfter := prop.State.SubAlloc(prop.ChannelParams.ID())
	if containedAfter {
		return errors.New("virtual channel must not be de-allocated after update")
	}

	// Assert correct balances
	virtual := transformBalances(prop.FinalState.Balances, parent.state().NumParts(), subAlloc.IndexMap)
	correctBalances := parent.state().Balances.Add(virtual).Equal(prop.State.Balances)
	if !correctBalances {
		return errors.New("invalid balances")
	}

	return nil
}

func (c *Client) matchSettlementProposal(a, b interface{}) (ok bool) {
	// Cast.
	inputs := []interface{}{a, b}
	props := make([]*virtualChannelSettlementProposal, len(inputs))
	for i, x := range inputs {
		var prop *virtualChannelSettlementProposal
		prop, ok = x.(*virtualChannelSettlementProposal)
		if !ok {
			return
		}
		props[i] = prop
	}

	prop0 := props[0]

	for _, prop := range props {
		// Check final state.
		ok = prop.FinalState.Equal(&prop0.FinalState) == nil
		if !ok {
			return
		}
	}

	// Store settlement state and signature.
	virtual, err := c.Channel(prop0.FinalState.ID)
	if err != nil {
		return false
	}
	if err = virtual.machine.Update(c.Ctx(), &prop0.FinalState, hubIndex); err != nil {
		return false
	}
	for i, sig := range prop0.FinalStateSignatures {
		if err = virtual.machine.AddSig(c.Ctx(), uint16(i), sig); err != nil {
			return false
		}
	}
	if err = virtual.machine.EnableFinal(c.Ctx()); err != nil {
		return false
	}

	// Close channel.
	err = virtual.Close()
	if err != nil {
		return false
	}
	c.channels.Delete(virtual.ID())
	return
}
