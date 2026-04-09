// Copyright 2026 - See NOTICE file for copyright holders.
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

package multi

import (
	"context"
	"sync"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
)

// CoordinationRequester sends an off-chain coordination request for a channel.
type CoordinationRequester interface {
	RequestCoordination(ctx context.Context, chID channel.ID, coordinator map[wallet.BackendID]wallet.Address) error
}

// CoordinationRegistry stores coordination waiters keyed by channel ID.
type CoordinationRegistry struct {
	mu        sync.Mutex
	waiters   map[channel.ID][]*coordinationWaiter
	ready     map[channel.ID]struct{}
	requester CoordinationRequester
}

type coordinationWaiter struct {
	ch      chan struct{}
	claimed bool
}

// NewCoordinationRegistry creates a new coordination registry.
func NewCoordinationRegistry() *CoordinationRegistry {
	return NewCoordinationRegistryWithRequester(nil)
}

// NewCoordinationRegistryWithRequester creates a new coordination registry.
func NewCoordinationRegistryWithRequester(requester CoordinationRequester) *CoordinationRegistry {
	return &CoordinationRegistry{
		waiters:   make(map[channel.ID][]*coordinationWaiter),
		ready:     make(map[channel.ID]struct{}),
		requester: requester,
	}
}

// SetRequester configures the outbound coordination requester.
func (r *CoordinationRegistry) SetRequester(requester CoordinationRequester) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requester = requester
}

// RequestCoordination registers a waiter for chID.
// If a requester is configured, it also sends an outbound off-chain request.
func (r *CoordinationRegistry) RequestCoordination(ctx context.Context, chID channel.ID, coordinator map[wallet.BackendID]wallet.Address) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	w := &coordinationWaiter{ch: make(chan struct{})}

	r.mu.Lock()
	_, coordinated := r.ready[chID]
	if !coordinated {
		r.waiters[chID] = append(r.waiters[chID], w)
	}
	requester := r.requester
	r.mu.Unlock()

	if coordinated {
		return nil
	}

	// Ensure canceled contexts do not leave stale waiters behind.
	go func() {
		select {
		case <-ctx.Done():
			r.removeWaiter(chID, w)
		case <-w.ch:
		}
	}()

	if requester != nil {
		err := requester.RequestCoordination(ctx, chID, coordinator)
		if err != nil {
			r.removeWaiter(chID, w)
			return err
		}
	}

	return nil
}

// AwaitCoordinated blocks until NotifyCoordinated is called for chID or ctx is canceled.
func (r *CoordinationRegistry) AwaitCoordinated(ctx context.Context, chID channel.ID) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	r.mu.Lock()
	_, coordinated := r.ready[chID]
	r.mu.Unlock()
	if coordinated {
		return nil
	}

	w := r.claimOrCreateWaiter(chID)
	select {
	case <-w.ch:
		return nil
	case <-ctx.Done():
		r.removeWaiter(chID, w)
		return ctx.Err()
	}
}

// NotifyCoordinated closes all waiters for chID and wakes any awaiting callers.
func (r *CoordinationRegistry) NotifyCoordinated(chID channel.ID) {
	r.mu.Lock()
	waiters := r.waiters[chID]
	delete(r.waiters, chID)
	r.ready[chID] = struct{}{}
	r.mu.Unlock()

	for _, w := range waiters {
		close(w.ch)
	}
}

func (r *CoordinationRegistry) claimOrCreateWaiter(chID channel.ID) *coordinationWaiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, w := range r.waiters[chID] {
		if !w.claimed {
			w.claimed = true
			return w
		}
	}

	w := &coordinationWaiter{ch: make(chan struct{}), claimed: true}
	r.waiters[chID] = append(r.waiters[chID], w)
	return w
}

func (r *CoordinationRegistry) removeWaiter(chID channel.ID, waiter *coordinationWaiter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	waiters := r.waiters[chID]
	for i, w := range waiters {
		if w == waiter {
			last := len(waiters) - 1
			waiters[i] = waiters[last]
			waiters = waiters[:last]
			if len(waiters) == 0 {
				delete(r.waiters, chID)
			} else {
				r.waiters[chID] = waiters
			}
			return
		}
	}
}
