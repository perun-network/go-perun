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
	"log"
	"math/big"
	"reflect"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	"perun.network/go-perun/backend/ethereum/bindings"
	"perun.network/go-perun/backend/ethereum/bindings/adjudicator"
	cherrors "perun.network/go-perun/backend/ethereum/channel/errors"
	"perun.network/go-perun/backend/ethereum/subscription"
	"perun.network/go-perun/backend/ethereum/wallet"
	"perun.network/go-perun/channel"
)

// Subscribe returns a new AdjudicatorSubscription to adjudicator events.
func (a *Adjudicator) Subscribe(ctx context.Context, chID channel.ID) (channel.AdjudicatorSubscription, error) {
	subs := []*AdjudicatorBackendSub{}
	for _, b := range a.backends {
		sub, err := b.Subscribe(ctx, chID)
		if err != nil {
			for _, sub := range subs {
				sub.Close()
			}
			return nil, err
		}
		subs = append(subs, sub)
	}

	return &AdjudicatorSub{
		subs: subs,
	}, nil
}

type AdjudicatorSub struct {
	subs []*AdjudicatorBackendSub
}

func (s AdjudicatorSub) Next() channel.AdjudicatorEvent {
	cases := make([]reflect.SelectCase, len(s.subs))
	for i, sub := range s.subs {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(sub.next)}
	}

	_, value, _ := reflect.Select(cases)
	if value.IsNil() {
		return nil
	}
	return value.Interface().(channel.AdjudicatorEvent)
}

func (s AdjudicatorSub) Err() error {
	cases := make([]reflect.SelectCase, len(s.subs))
	for i, sub := range s.subs {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(sub.err)}
	}

	for len(cases) > 0 {
		i, value, _ := reflect.Select(cases)
		cases = append(cases[:i], cases[i+1:]...)
		if !value.IsNil() {
			return value.Interface().(error)
		}
	}
	return nil
}

func (s AdjudicatorSub) Close() error {
	for _, sub := range s.subs {
		sub.Close()
	}
	return nil
}

func (b adjudicatorBackend) Subscribe(ctx context.Context, chID channel.ID) (*AdjudicatorBackendSub, error) {
	subErr := make(chan error, 1)
	events := make(chan *subscription.Event, adjEventBuffSize)
	eFact := func() *subscription.Event {
		return &subscription.Event{
			Name:   bindings.Events.AdjChannelUpdate,
			Data:   new(adjudicator.AdjudicatorChannelUpdate),
			Filter: [][]interface{}{{chID}},
		}
	}
	sub, err := subscription.Subscribe(ctx, b.backend, b.bound, eFact, startBlockOffset, b.backend.txFinalityDepth)
	if err != nil {
		return nil, errors.WithMessage(err, "creating filter-watch event subscription")
	}
	// Find new events
	go func() {
		subErr <- sub.Read(ctx, events)
	}()
	rsub := &AdjudicatorBackendSub{
		backend: b,
		sub:     sub,
		subErr:  subErr,
		next:    make(chan channel.AdjudicatorEvent, 1),
		err:     make(chan error, 1),
	}
	go rsub.updateNext(ctx, events)

	return rsub, nil
}

// AdjudicatorBackendSub implements the channel.AdjudicatorSubscription interface.
type AdjudicatorBackendSub struct {
	backend adjudicatorBackend
	sub     *subscription.ResistantEventSub // Event subscription
	subErr  chan error
	next    chan channel.AdjudicatorEvent // Event sink
	err     chan error                    // error from subscription
}

func (r *AdjudicatorBackendSub) updateNext(ctx context.Context, events chan *subscription.Event) {
evloop:
	for {
		select {
		case _next := <-events:
			err := r.processNext(ctx, _next)
			if err != nil {
				r.err <- err
				break evloop
			}
		case err := <-r.subErr:
			if err != nil {
				r.err <- errors.WithMessage(err, "EventSub closed")
			} else {
				// Normal closing should produce no error
				close(r.err)
			}
			break evloop
		}
	}

	// subscription got closed, close next channel and return
	select {
	case <-r.next:
	default:
	}
	close(r.next)
}

func (r *AdjudicatorBackendSub) processNext(ctx context.Context, _next *subscription.Event) (err error) {
	next, ok := _next.Data.(*adjudicator.AdjudicatorChannelUpdate)
	next.Raw = _next.Log
	if !ok {
		log.Panicf("unexpected event type: %T", _next.Data)
	}

	select {
	// drain next-channel on new event
	case current := <-r.next:
		currentTimeout, ok := current.Timeout().(*BlockTimeout)
		if !ok {
			log.Panic("wrong timeout type")
		}
		// if newer version or same version and newer timeout, replace
		if current.Version() < next.Version || current.Version() == next.Version && currentTimeout.Time < next.Timeout {
			var e channel.AdjudicatorEvent
			e, err = r.backend.convertEvent(ctx, next)
			if err != nil {
				return
			}

			r.next <- e
		} else { // otherwise, reuse old
			r.next <- current
		}
	default: // next-channel is empty
		var e channel.AdjudicatorEvent
		e, err = r.backend.convertEvent(ctx, next)
		if err != nil {
			return
		}

		r.next <- e
	}
	return err
}

// Next returns the newest past or next blockchain event.
// It blocks until an event is returned from the blockchain or the subscription
// is closed. If the subscription is closed, Next immediately returns nil.
// If there was a past event when the subscription was set up, the first call to
// Next will return it.
func (r *AdjudicatorBackendSub) Next() channel.AdjudicatorEvent {
	reg := <-r.next
	if reg == nil {
		return nil // otherwise we get (*RegisteredEvent)(nil)
	}
	return reg
}

// Close closes this subscription. Any pending calls to Next will return nil.
func (r *AdjudicatorBackendSub) Close() error {
	r.sub.Close()
	return nil
}

// Err returns the error of the event subscription.
// Should only be called after Next returned nil.
func (r *AdjudicatorBackendSub) Err() error {
	return <-r.err
}

func (b adjudicatorBackend) convertEvent(ctx context.Context, e *adjudicator.AdjudicatorChannelUpdate) (channel.AdjudicatorEvent, error) {
	base := channel.NewAdjudicatorEventBase(e.ChannelID, NewBlockTimeout(b.backend, e.Timeout), e.Version)
	switch e.Phase {
	case phaseDispute:
		args, err := b.fetchRegisterCallData(ctx, e.Raw.TxHash)
		if err != nil {
			return nil, errors.WithMessage(err, "fetching call data")
		}

		ch, ok := args.signedState(e.ChannelID)
		if !ok {
			return nil, errors.Errorf("channel not found in calldata: %v", e.ChannelID)
		}

		var app channel.App
		var zeroAddress common.Address
		if ch.Params.App == zeroAddress {
			app = channel.NoApp()
		} else {
			app, err = channel.Resolve(wallet.AsWalletAddr(ch.Params.App))
			if err != nil {
				return nil, err
			}
		}
		state := FromEthState(app, &ch.State)

		return &channel.RegisteredEvent{
			AdjudicatorEventBase: *base,
			State:                &state,
			Sigs:                 ch.Sigs,
		}, nil

	case phaseForceExec:
		args, err := b.fetchProgressCallData(ctx, e.Raw.TxHash)
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

func (b adjudicatorBackend) fetchProgressCallData(ctx context.Context, txHash common.Hash) (*progressCallData, error) {
	var args progressCallData
	err := b.fetchCallData(ctx, txHash, abiProgress, &args)
	return &args, errors.WithMessage(err, "fetching call data")
}

type registerCallData struct {
	Channel     adjudicator.AdjudicatorSignedState
	SubChannels []adjudicator.AdjudicatorSignedState
}

func (args *registerCallData) signedState(id channel.ID) (*adjudicator.AdjudicatorSignedState, bool) {
	ch := &args.Channel
	if ch.State.ChannelID == id {
		return ch, true
	}
	for _, ch := range args.SubChannels {
		if ch.State.ChannelID == id {
			return &ch, true
		}
	}
	return nil, false
}

func (b adjudicatorBackend) fetchRegisterCallData(ctx context.Context, txHash common.Hash) (*registerCallData, error) {
	var args registerCallData
	err := b.fetchCallData(ctx, txHash, abiRegister, &args)
	return &args, errors.WithMessage(err, "fetching call data")
}

func (b adjudicatorBackend) fetchCallData(ctx context.Context, txHash common.Hash, method abi.Method, args interface{}) error {
	tx, _, err := b.backend.TransactionByHash(ctx, txHash)
	if err != nil {
		err = cherrors.CheckIsChainNotReachableError(err)
		return errors.WithMessage(err, "getting transaction")
	}

	argsData := tx.Data()[len(method.ID):]

	argsI, err := method.Inputs.UnpackValues(argsData)
	if err != nil {
		return errors.WithMessage(err, "unpacking")
	}

	err = method.Inputs.Copy(args, argsI)
	if err != nil {
		return errors.WithMessage(err, "copying into struct")
	}

	return nil
}
