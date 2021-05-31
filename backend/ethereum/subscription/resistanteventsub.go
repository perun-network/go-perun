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

package subscription

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/pkg/errors"
	cherrors "perun.network/go-perun/backend/ethereum/channel/errors"
	"perun.network/go-perun/log"
)

type (
	// Wraps an `EventSub` and makes it resistant to chain reorgs.
	// It handles `removed` and `rebirth` events and has a `finalityDepth`
	// threshold to decide when an Event is final.
	// Its will never emit the same event twice unless the connected geth node
	// is faulty.
	ResistantEventSub struct {
		sub           *EventSub
		finalityDepth uint64
		closed        chan struct{}

		lastBlockNum *big.Int
		heads        chan *types.Header
		headSub      ethereum.Subscription
		events       map[common.Hash]*Event
	}
)

// NewResistantEventSub creates a new `ResistantEventSub` from the given
// `EventSub`. Closes the passed `EventSub` when done.
// The passed `EventSub` should query at least `finalityDepth` blocks into
// the past.
func NewResistantEventSub(ctx context.Context, sub *EventSub, cr ethereum.ChainReader, finalityDepth uint64) (*ResistantEventSub, error) {
	if finalityDepth < 0 {
		// TODO
		panic("finalityDepth needs to be at least 2")
	}
	last, err := cr.HeaderByNumber(ctx, nil)
	if err != nil {
		err = cherrors.CheckIsChainNotReachableError(err)
		return nil, errors.WithMessage(err, "subscribing to headers")
	}
	log.Debugf("Resistant Event sub started at block: %v", last.Number)
	// Use a large buffer to not block geth.
	heads := make(chan *types.Header, 128)
	headSub, err := cr.SubscribeNewHead(ctx, heads)
	if err != nil {
		headSub.Unsubscribe()
		err = cherrors.CheckIsChainNotReachableError(err)
		return nil, errors.WithMessage(err, "subscribing to headers")
	}

	return &ResistantEventSub{
		sub:           sub,
		lastBlockNum:  last.Number,
		heads:         heads,
		headSub:       headSub,
		finalityDepth: finalityDepth,
		closed:        make(chan struct{}),
		events:        make(map[common.Hash]*Event),
	}, nil
}

// ReadAll reads all past and future events into `sink`.
// Can be aborted by cancelling `ctx` or `Close()`.
// All events can be considered final.
func (s *ResistantEventSub) ReadAll(_ctx context.Context, sink chan<- *Event) error {
	ctx, cancel := context.WithCancel(_ctx)
	defer cancel()
	subErr := make(chan error, 1)
	rawEvents := make(chan *Event, 128)
	// Read events from the underlying event subscription.
	go func() {
		subErr <- s.sub.ReadAll(ctx, rawEvents)
	}()

	for {
		select {
		case head := <-s.heads:
			if head == nil {
				return errors.New("head sub returned nil")
			}
			s.processHead(head, sink)
		case event := <-rawEvents:
			s.processEvent(event, sink)
		case e := <-s.headSub.Err():
			return errors.WithMessage(e, "underlying head subscription")
		case e := <-subErr:
			if e != nil {
				return errors.WithMessage(e, "underlying EventSub.Read")
			}
			return errors.New("underlying event sub terminated")
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return nil
		}
	}
}

func (s *ResistantEventSub) ReadPast(_ctx context.Context, sink chan<- *Event) error {
	ctx, cancel := context.WithCancel(_ctx)
	defer cancel()
	subErr := make(chan error, 1)
	rawEvents := make(chan *Event, 128)
	// Read events from the underlying event subscription.
	go func() {
		defer close(rawEvents)
		subErr <- s.sub.ReadPast(ctx, rawEvents)
	}()

	for {
		select {
		case head := <-s.heads:
			if head == nil {
				return errors.New("head sub returned nil")
			}
			s.processHead(head, sink)
		case event := <-rawEvents:
			if event == nil {
				return nil
			}
			s.processEvent(event, sink)
		case e := <-s.headSub.Err():
			return errors.WithMessage(e, "underlying head subscription")
		case e := <-subErr:
			if e != nil {
				return errors.WithMessage(e, "underlying EventSub.Read")
			}
			continue
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return nil
		}
	}
}

func (s *ResistantEventSub) processEvent(event *Event, sink chan<- *Event) {
	hash := event.Log.TxHash
	log := log.WithField("hash", hash.Hex())
	// EventSub can emit events twice.
	if _, found := s.events[hash]; found {
		log.Warn("Received same event twice, ignored")
		return
	}

	if event.Log.Removed {
		log.Trace("Event preliminary excluded")
		delete(s.events, hash)
	} else {
		if s.isFinal(event) {
			sink <- event
			delete(s.events, hash)
		} else {
			log.Trace("Event preliminary included")
			s.events[hash] = event
		}
	}
}

func (s *ResistantEventSub) processHead(head *types.Header, sink chan<- *Event) {
	diff := head.Number.Uint64() - s.lastBlockNum.Uint64()
	switch diff {
	case 0:
		// NOTE this needs to be changed in the reorg case.
		log.Warn("Received same block twice, ignored")
		return
	case 1:
		log.Tracef("Received new block %v", head.Number)
	default:
		log.Panicf("Block number out of order. Last: %v, Now: %v, increment: %v", s.lastBlockNum, head.Number, diff)
	}
	s.lastBlockNum.Set(head.Number)

	// Go through all event and check for finality.
	for _, event := range s.events {
		if s.isFinal(event) {
			sink <- event
			delete(s.events, event.Log.TxHash)
		}
	}
}

func (s *ResistantEventSub) isFinal(event *Event) bool {
	log := log.WithField("hash", event.Log.TxHash.Hex())
	diff := s.lastBlockNum.Uint64() - event.Log.BlockNumber

	if event.Log.BlockNumber > s.lastBlockNum.Uint64() {
		log.Tracef("Event sub was faster than head sub, ignored")
		return false
	} else if diff >= s.finalityDepth {
		log.Debug("Event final after %d block(s)", diff)
		return true
	} else {
		log.Tracef("Event included %d time(s)", diff)
		return false
	}
}

// Close closes the sub and frees associated resources.
// Should be called exactly once and panics otherwise.
func (s *ResistantEventSub) Close() {
	close(s.closed)
	s.headSub.Unsubscribe()
	s.sub.Close()
}
