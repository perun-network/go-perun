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
	"math/big"
	"sync"

	"perun.network/go-perun/channel"
)

func (c *Channel) translateBalances(indexMap []channel.Index) channel.Balances {
	state := c.state()
	return transformBalances(state.Balances, state.NumParts(), indexMap)
}

func transformBalances(b channel.Balances, numParts int, indexMap []channel.Index) (_b channel.Balances) {
	_b = make(channel.Balances, len(b))
	for a := range _b {
		_b[a] = make([]*big.Int, numParts)
		// Init with zero.
		for p := range _b[a] {
			_b[a][p] = big.NewInt(0)
		}
		// Fill at specified indices.
		for p, _p := range indexMap {
			_b[a][_p] = b[a][p]
		}
	}
	return
}

func (c *Client) rejectProposal(responder *UpdateResponder, reason string) {
	ctx, cancel := context.WithTimeout(c.Ctx(), responseTimeout)
	defer cancel()
	err := responder.Reject(ctx, reason)
	if err != nil {
		c.log.Warnln(err)
	}
}

func (c *Client) acceptProposal(responder *UpdateResponder) {
	ctx, cancel := context.WithTimeout(c.Ctx(), responseTimeout)
	defer cancel()
	err := responder.Accept(ctx)
	if err != nil {
		c.log.Warnln(err)
	}
}

type watcherEntry struct {
	state interface{}
	done  chan struct{}
}

type stateWatcher struct {
	sync.RWMutex
	entries   map[uint]watcherEntry
	counter   uint
	condition func(a, b interface{}) bool
}

// Await blocks until the condition is met. For any set of matching states, the
// condition function is evaluated at most once.
func (w *stateWatcher) Await(
	ctx context.Context,
	state interface{},
) (err error) {
	match := make(chan struct{}, 1)
	id := w.nextID()
	w.register(id, state, match)
	defer w.deregister(id)
	select {
	case <-match:
	case <-ctx.Done():
		err = ctx.Err()
	}
	return
}

func (w *stateWatcher) register(
	id uint,
	state interface{},
	done chan struct{},
) {
	w.Lock()
	defer w.Unlock()

	for k, e := range w.entries {
		if w.condition(state, e.state) {
			done <- struct{}{}
			e.done <- struct{}{}
			delete(w.entries, k)
			return
		}
	}

	w.entries[id] = watcherEntry{state: state, done: done}
}

func (w *stateWatcher) nextID() (id uint) {
	w.Lock()
	defer w.Unlock()

	id = w.counter
	w.counter++
	if w.counter < id {
		panic("overflow")
	}
	return
}

func (w *stateWatcher) deregister(
	id uint,
) {
	w.Lock()
	defer w.Unlock()

	delete(w.entries, id)
}
