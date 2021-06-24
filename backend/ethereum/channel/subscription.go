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

package channel

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	"perun.network/go-perun/backend/ethereum/bindings"
	"perun.network/go-perun/backend/ethereum/bindings/adjudicator"
	cherrors "perun.network/go-perun/backend/ethereum/channel/errors"
	"perun.network/go-perun/backend/ethereum/subscription"
	"perun.network/go-perun/backend/ethereum/wallet"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/log"
	pkgsync "perun.network/go-perun/pkg/sync"
)

// RegisteredSub implements the channel.AdjudicatorSubscription interface.
type RegisteredSub struct {
	closer pkgsync.Closer

	adj *Adjudicator
	cr  ethereum.ChainReader // chain reader to read block time

	sub    *subscription.EventSub        // Event subscription
	subErr chan error                    // contains errors from updateNext()
	sink   chan channel.AdjudicatorEvent // will be read by Next()
}

var _ channel.AdjudicatorSubscription = &RegisteredSub{}

// Subscribe returns a new AdjudicatorSubscription to adjudicator events.
func (a *Adjudicator) Subscribe(ctx context.Context, params *channel.Params) (channel.AdjudicatorSubscription, error) {
	eFact := func() *subscription.Event {
		return &subscription.Event{
			Name:   bindings.Events.AdjChannelUpdate,
			Data:   new(adjudicator.AdjudicatorChannelUpdate),
			Filter: [][]interface{}{{params.ID()}},
		}
	}
	sub, err := subscription.NewEventSub(ctx, a.ContractBackend, a.bound, eFact, startBlockOffset)
	if err != nil {
		return nil, errors.WithMessage(err, "creating filter-watch event subscription")
	}
	rsub := &RegisteredSub{
		adj:    a,
		cr:     a.ContractInterface,
		sub:    sub,
		subErr: make(chan error, 1),
		sink:   make(chan channel.AdjudicatorEvent, 10),
	}
	go func() {
		err := rsub.updateNext()
		log.WithError(err).Debug("updateNext returned")
		rsub.subErr <- err

	}()
	return rsub, nil
}

// Next returns the newest past or next blockchain event.
// It blocks until an event is returned from the blockchain or the subscription
// is closed. If the subscription is closed, Next immediately errors.
// If there was a past event when the subscription was set up, the first call to
// Next will return it.
func (r *RegisteredSub) Next() (channel.AdjudicatorEvent, error) {
	select {
	case event := <-r.sink:
		if event == nil {
			log.Panic("nil event")
		}
		return event, nil
	case <-r.closer.Closed():
		return nil, errors.New("closed")
	}
}

func (r *RegisteredSub) updateNext() error {
	// Return the last past event, if any.
	events := make(chan *subscription.Event, 10)
	subErr := make(chan error, 1)
	// Read all past events.
	go func() {
		defer close(events)
		subErr <- r.sub.ReadPast(r.closer.Ctx(), events)
	}()
	// Get the last past event, if any.
	last := <-events
	for e := range events {
		last = e
	}
	if err := <-subErr; err != nil {
		return errors.WithMessage(err, "reading past events")
	}
	// Returns the last past event, if any.
	if last != nil {
		e, err := r.adj.convertEvent(r.closer.Ctx(), castEvent(last), last.Log.TxHash)
		if err != nil {
			return errors.WithMessage(err, "converting event")
		}
		select {
		case r.sink <- e:
		case <-r.closer.Closed():
			return nil
		}
	}

	// Return all future event, block afterwards.
	events = make(chan *subscription.Event, 10)
	subErr = make(chan error, 1)
	// Read all future events.
	go func() {
		subErr <- r.sub.Read(r.closer.Ctx(), events)
	}()
	// Block until an event arrives.
	for {
		select {
		case _e := <-events:
			e, err := r.adj.convertEvent(r.closer.Ctx(), castEvent(_e), _e.Log.TxHash)
			if err != nil {
				return errors.WithMessage(err, "converting event")
			}

			select {
			case r.sink <- e:
			case <-r.closer.Closed():
				return nil
			}
		case err := <-subErr:
			return errors.WithMessage(err, "reading future events")
		case <-r.closer.Closed():
			return nil
		}
	}
}

func castEvent(e *subscription.Event) *adjudicator.AdjudicatorChannelUpdate {
	return e.Data.(*adjudicator.AdjudicatorChannelUpdate)
}

// Close closes this subscription. Any pending calls to Next will error.
// Can be called more than once.
func (r *RegisteredSub) Close() error {
	if err := r.closer.Close(); err == nil {
		return <-r.subErr // wait for updateNext() to return
	} else if pkgsync.IsAlreadyClosedError(err) {
		return nil
	} else {
		return errors.WithMessage(err, "closing Closer")
	}
}

func (a *Adjudicator) convertEvent(ctx context.Context, e *adjudicator.AdjudicatorChannelUpdate, txHash common.Hash) (channel.AdjudicatorEvent, error) {
	base := channel.NewAdjudicatorEventBase(e.ChannelID, NewBlockTimeout(a.ContractInterface, e.Timeout), e.Version)
	switch e.Phase {
	case phaseDispute:
		return &channel.RegisteredEvent{AdjudicatorEventBase: *base}, nil

	case phaseForceExec:
		args, err := a.fetchProgressCallData(ctx, txHash)
		if err != nil {
			return nil, errors.WithMessage(err, "fetching call data")
		}
		app, err := channel.Resolve(wallet.AsWalletAddr(args.Params.App))
		if err != nil {
			return nil, errors.WithMessage(err, "resolving app")
		}
		newState := FromEthState(app, &args.State)
		return &channel.ProgressedEvent{
			AdjudicatorEventBase: *base,
			State:                &newState,
			Idx:                  channel.Index(args.ActorIdx.Uint64()),
		}, nil

	case phaseConcluded:
		return &channel.ConcludedEvent{AdjudicatorEventBase: *base}, nil

	default:
		panic("unknown phase")
	}
}

type progressCallData struct {
	Params   adjudicator.ChannelParams
	StateOld adjudicator.ChannelState
	State    adjudicator.ChannelState
	ActorIdx *big.Int
	Sig      []byte
}

func (a *Adjudicator) fetchProgressCallData(ctx context.Context, txHash common.Hash) (*progressCallData, error) {
	tx, _, err := a.ContractBackend.TransactionByHash(ctx, txHash)
	if err != nil {
		err = cherrors.CheckIsChainNotReachableError(err)
		return nil, errors.WithMessage(err, "getting transaction")
	}

	argsData := tx.Data()[len(abiProgress.ID):]

	argsI, err := abiProgress.Inputs.UnpackValues(argsData)
	if err != nil {
		return nil, errors.WithMessage(err, "unpacking")
	}

	var args progressCallData
	err = abiProgress.Inputs.Copy(&args, argsI)
	if err != nil {
		return nil, errors.WithMessage(err, "copying into struct")
	}

	return &args, nil
}
