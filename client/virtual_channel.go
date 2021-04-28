package client

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/errors"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wire"
)

func (c *Client) fundVirtualChannel(ctx context.Context, ch *Channel, prop *VirtualChannelProposal) (err error) {
	parent, ok := c.channels.Get(prop.Parent)
	if !ok {
		return errors.New("referenced parent channel not found")
	}

	switch ch.Idx() {
	case proposerIdx, proposeeIdx:
		err := parent.proposeVirtualChannelFunding(ctx, ch)
		if err != nil {
			return errors.WithMessage(err, "registering channel funding")
		}
	default:
		return errors.New("invalid participant index")
	}

	return c.completeFunding(ctx, ch)
}

func (ch *Channel) proposeVirtualChannelFunding(ctx context.Context, virtual *Channel) (err error) {
	// Lock machine while update is in progress.
	if !ch.machMtx.TryLockCtx(ctx) {
		return errors.Errorf("locking machine mutex in time: %v", ctx.Err())
	}
	defer ch.machMtx.Unlock()

	state := ch.machine.State().Clone()
	// Deposit initial balances into sub-allocation
	balances := virtual.State().Balances
	state.Allocation.Balances = state.Allocation.Balances.Sub(balances)
	state.AddSubAlloc(*channel.NewSubAlloc(virtual.ID(), balances.Sum()))

	err = ch.updateGeneric(ctx, state, func(mcu *msgChannelUpdate) wire.Msg {
		return &virtualChannelFundingProposal{
			msgChannelUpdate:      *mcu,
			ChannelParams:         *virtual.Params(),
			VersionZeroState:      *virtual.State(),
			VersionZeroSignatures: virtual.machine.CurrentTX().Sigs,
		}
	})
	return
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

	err = c.awaitMatchingVirtualChannelFundingProposal(ctx, parent, prop)
	if err != nil {
		c.rejectProposal(responder, err.Error())
	}

	c.acceptProposal(responder)
}

func (c *Client) validateVirtualChannelFundingProposal(
	parent *Channel,
	prop *virtualChannelFundingProposal,
) error {
	if prop.ChannelParams.ID() != prop.VersionZeroState.ID {
		return errors.New("invalid parameters")
	}

	for i, sig := range prop.VersionZeroSignatures {
		ok, err := channel.Verify(
			prop.ChannelParams.Parts[i],
			&prop.ChannelParams,
			&prop.VersionZeroState,
			sig,
		)
		if err != nil {
			return err
		} else if !ok {
			return errors.New("invalid signature")
		}
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

func (c *Client) awaitMatchingVirtualChannelFundingProposal(
	ctx context.Context,
	parent *Channel,
	prop *virtualChannelFundingProposal,
) error {
	done := make(chan struct{}, 0)
	c.fundingProposalWatchers.Register(prop.State.ID, done)
	defer c.fundingProposalWatchers.Deregister(prop.State.ID)
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

type VirtualChannelFundingProposalWatchers struct {
	entries map[channel.ID]chan struct{}
	sync.RWMutex
}

func (w *VirtualChannelFundingProposalWatchers) Register(
	channelID channel.ID,
	done chan struct{},
) {
	w.Lock()
	defer w.Unlock()

	_done, ok := w.entries[channelID]
	if ok {
		done <- struct{}{}
		_done <- struct{}{}
		delete(w.entries, channelID)
		return
	}

	w.entries[channelID] = done
}

func (w *VirtualChannelFundingProposalWatchers) Deregister(
	channelID channel.ID,
) {
	w.Lock()
	defer w.Unlock()

	_, ok := w.entries[channelID]
	if ok {
		delete(w.entries, channelID)
		return
	}
}
