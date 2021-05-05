package client

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/errors"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
	"perun.network/go-perun/wire"
)

func (c *Client) fundVirtualChannel(ctx context.Context, virtual *Channel, prop *VirtualChannelProposal) (err error) {
	var parentID channel.ID
	switch virtual.Idx() {
	case proposerIdx:
		parentID = prop.Parent
	case proposeeIdx:
		parentID = prop.ParentReceiver
	default:
		return errors.New("invalid participant index")
	}

	parent, ok := c.channels.Get(parentID)
	if !ok {
		return errors.New("referenced parent channel not found")
	}

	switch virtual.Idx() {
	case proposerIdx, proposeeIdx:
		err := parent.proposeVirtualChannelFunding(ctx, virtual)
		if err != nil {
			return errors.WithMessage(err, "registering channel funding")
		}
	default:
		return errors.New("invalid participant index")
	}

	return c.completeFunding(ctx, virtual)
}

func (ch *Channel) proposeVirtualChannelFunding(ctx context.Context, virtual *Channel) (err error) {
	// Lock machine while update is in progress.
	if !ch.machMtx.TryLockCtx(ctx) {
		return errors.Errorf("locking machine mutex in time: %v", ctx.Err())
	}
	defer ch.machMtx.Unlock()

	state := ch.machine.State().Clone()
	state.Version++

	// Deposit initial balances into sub-allocation
	m := remapMapForVirtualChannel(ch.Params().Parts, virtual.Params().Parts)
	balances := virtual.State().Balances.Remap(m)
	state.Allocation.Balances = state.Allocation.Balances.Sub(balances)
	state.AddSubAlloc(*channel.NewSubAlloc(virtual.ID(), balances.Sum()))

	err = ch.updateGeneric(ctx, state, func(mcu *msgChannelUpdate) wire.Msg {
		return &virtualChannelFundingProposal{
			msgChannelUpdate:       *mcu,
			ChannelParams:          *virtual.Params(),
			InitialState:           *virtual.State(),
			InitialStateSignatures: virtual.machine.CurrentTX().Sigs,
		}
	})
	return
}

func remapMapForVirtualChannel(parent, virtual []wallet.Address) map[int]int {
	if len(parent) != 2 || len(virtual) != 2 {
		panic("only implemented for two-party channels")
	}

	if parent[0].Equals(virtual[0]) || parent[1].Equals(virtual[1]) {
		return map[int]int{0: 0, 1: 1}
	} else if parent[0].Equals(virtual[1]) || parent[1].Equals(virtual[0]) {
		return map[int]int{0: 1, 1: 0}
	}

	panic("invalid participants")
}

const messageTimeout = 10 * time.Second
const virtualChannelFundingTimeout = 10 * time.Second

func (c *Client) handleVirtualChannelFundingProposal(
	parent *Channel,
	prop *virtualChannelFundingProposal,
	responder *UpdateResponder,
) {
	err := c.validateVirtualChannelFundingProposal(parent, prop)
	if err != nil {
		c.rejectProposal(responder, err.Error())
	}

	ctx, cancel := context.WithTimeout(c.Ctx(), virtualChannelFundingTimeout)
	defer cancel()

	err = c.hub.send(&prop.InitialState)
	c.log.WithError(err).Info("Sending init state to hub")
	err = c.awaitMatchingVirtualChannelState(ctx, &prop.InitialState)
	if err != nil {
		c.rejectProposal(responder, err.Error())
	}

	//TODO store state and signatures

	c.acceptProposal(responder)
}

func (c *Client) validateVirtualChannelFundingProposal(
	parent *Channel,
	prop *virtualChannelFundingProposal,
) error {
	// Validate parameters.
	if prop.ChannelParams.ID() != prop.InitialState.ID {
		return errors.New("invalid parameters")
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

	// Validate allocation.

	// Assert equal assets.
	if err := channel.AssetsAssertEqual(parent.state().Assets, prop.InitialState.Assets); err != nil {
		return errors.WithMessage(err, "assets do not match")
	}

	// Assert sufficient funds in parent channel.
	m := remapMapForVirtualChannel(parent.Params().Parts, prop.ChannelParams.Parts)
	virtual := prop.InitialState.Balances.Remap(m)
	if err := parent.state().Balances.AssertGreaterOrEqual(virtual); err != nil {
		return errors.WithMessage(err, "insufficient funds")
	}

	// Assert not contained before
	_, containedBefore := parent.state().SubAlloc(prop.ChannelParams.ID())
	if containedBefore {
		return errors.New("virtual channel already allocated")
	}

	// Assert contained after with correct balances
	expected := channel.SubAlloc{ID: prop.ChannelParams.ID(), Bals: virtual.Sum()}
	subAlloc, containedAfter := prop.State.SubAlloc(expected.ID)
	if !containedAfter || subAlloc.Equal(&expected) != nil {
		return errors.New("invalid allocation")
	}

	return nil
}

func (c *Client) rejectProposal(responder *UpdateResponder, reason string) {
	ctx, cancel := context.WithTimeout(c.Ctx(), messageTimeout)
	defer cancel()
	err := responder.Reject(ctx, reason)
	if err != nil {
		c.log.Warnln(err)
	}
}

func (c *Client) acceptProposal(responder *UpdateResponder) {
	ctx, cancel := context.WithTimeout(c.Ctx(), messageTimeout)
	defer cancel()
	err := responder.Accept(ctx)
	if err != nil {
		c.log.Warnln(err)
	}
}

func (c *Client) awaitMatchingVirtualChannelState(
	ctx context.Context,
	state *channel.State,
) error {
	done := make(chan struct{}, 1)
	c.stateWatcher.Register(state, done)

	defer c.stateWatcher.Deregister(state.ID)
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

type StateAndDone struct {
	state *channel.State
	done  chan struct{}
}

type VirtualChannelFundingProposalWatchers struct {
	entries map[channel.ID]StateAndDone
	sync.RWMutex
}

func (w *VirtualChannelFundingProposalWatchers) Register(
	state *channel.State,
	done chan struct{},
) {
	w.Lock()
	defer w.Unlock()

	channelID := state.ID

	e, ok := w.entries[channelID]
	if ok && e.state.Equal(state) == nil {
		if done != nil {
			close(done)
		}
		if e.done != nil {
			close(e.done)
		}
		delete(w.entries, channelID)
		return
	}

	w.entries[channelID] = StateAndDone{state: state, done: done}
}

func (w *VirtualChannelFundingProposalWatchers) Deregister(
	channelID channel.ID,
) {
	w.Lock()
	defer w.Unlock()

	delete(w.entries, channelID)
}

func (c *Channel) withdrawVirtualChannelIntoParent(ctx context.Context) error {
	if !c.IsVirtualChannel() {
		c.Log().Panic("not a virtual channel")
	} else if !c.machine.State().IsFinal {
		return errors.New("not final")
	}

	err := c.Parent().withdrawVirtualChannel(ctx, c)
	return errors.WithMessage(err, "updating parent channel")
}

// withdrawVirtualChannel proposes to release the funds allocated to the
// specified virtual channel.
func (ch *Channel) withdrawVirtualChannel(ctx context.Context, virtual *Channel) (err error) {
	// Lock machine while update is in progress.
	if !ch.machMtx.TryLockCtx(ctx) {
		return errors.Errorf("locking machine mutex in time: %v", ctx.Err())
	}
	defer ch.machMtx.Unlock()

	state := ch.machine.State().Clone()
	state.Version++

	virtualAlloc, ok := state.SubAlloc(virtual.ID())
	if !ok {
		ch.Log().Panicf("sub-allocation %x not found", virtualAlloc.ID)
	}

	if !virtualAlloc.BalancesEqual(virtual.state().Allocation.Sum()) {
		ch.Log().Panic("sub-allocation does not equal accumulated sub-channel outcome")
	}

	m := remapMapForVirtualChannel(ch.Params().Parts, virtual.Params().Parts)
	virtualBalsRemapped := virtual.state().Balances.Remap(m)

	// We assume that the asset types of parent channel and virtual channel are the same.
	for a, assetBalances := range virtualBalsRemapped {
		for u, userBalance := range assetBalances {
			parentBalance := state.Allocation.Balances[a][u]
			parentBalance.Add(parentBalance, userBalance)
		}
	}

	if err := state.Allocation.RemoveSubAlloc(virtualAlloc); err != nil {
		ch.Log().WithError(err).Panicf("removing sub-allocation with id %x", virtualAlloc.ID)
	}

	err = ch.updateGeneric(ctx, state, func(mcu *msgChannelUpdate) wire.Msg {
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

	err = c.hub.send(&prop.FinalState)
	c.log.WithError(err).Info("Sending final state to hub")
	err = c.awaitMatchingVirtualChannelState(ctx, &prop.FinalState)
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
	m := remapMapForVirtualChannel(parent.Params().Parts, prop.ChannelParams.Parts)
	virtual := prop.FinalState.Balances.Remap(m)
	if !containedBefore || !subAlloc.BalancesEqual(virtual.Sum()) {
		return errors.New("virtual channel not allocated")
	}

	// Assert not contained after
	_, containedAfter := prop.State.SubAlloc(prop.ChannelParams.ID())
	if containedAfter {
		return errors.New("virtual channel must not be de-allocated after update")
	}

	// Assert correct balances
	correctBalances := parent.state().Balances.Add(virtual).Equal(prop.State.Balances)
	if !correctBalances {
		return errors.New("invalid balances")
	}

	return nil
}
