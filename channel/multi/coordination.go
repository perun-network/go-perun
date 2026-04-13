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
	expected  map[channel.ID]int
	received  map[channel.ID]int
	seen      map[channel.ID]map[LedgerBackendKey]struct{}
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
		expected:  make(map[channel.ID]int),
		received:  make(map[channel.ID]int),
		seen:      make(map[channel.ID]map[LedgerBackendKey]struct{}),
		requester: requester,
	}
}

// SetExpectedCoordinatedEvents configures how many CoordinatedEvents must be
// observed for a channel before waiters are released.
func (r *CoordinationRegistry) SetExpectedCoordinatedEvents(chID channel.ID, expected int) {
	if expected < 1 {
		expected = 1
	}

	r.mu.Lock()
	if _, coordinated := r.ready[chID]; coordinated {
		r.mu.Unlock()
		return
	}

	r.expected[chID] = expected

	var waiters []*coordinationWaiter
	if r.received[chID] >= expected {
		waiters = r.markReadyLocked(chID)
	}
	r.mu.Unlock()

	r.releaseWaiters(waiters)
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
	if _, ok := r.expected[chID]; !ok {
		// Preserve legacy one-event behavior if no explicit threshold was set.
		r.expected[chID] = 1
	}

	var waiters []*coordinationWaiter
	if r.received[chID] >= r.expected[chID] {
		waiters = r.markReadyLocked(chID)
	}

	_, coordinated := r.ready[chID]
	if !coordinated {
		r.waiters[chID] = append(r.waiters[chID], w)
	}
	requester := r.requester
	r.mu.Unlock()

	r.releaseWaiters(waiters)

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
	if _, ok := r.expected[chID]; !ok {
		// Preserve legacy one-event behavior if no explicit threshold was set.
		r.expected[chID] = 1
	}

	var waiters []*coordinationWaiter
	if r.received[chID] >= r.expected[chID] {
		waiters = r.markReadyLocked(chID)
	}

	_, coordinated := r.ready[chID]
	r.mu.Unlock()

	r.releaseWaiters(waiters)
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

// NotifyCoordinated records one observed CoordinatedEvent for chID.
// Waiters are released only once the expected event count is reached.
//
// Prefer NotifyCoordinatedFromLedger when the event source ledger is known.
func (r *CoordinationRegistry) NotifyCoordinated(chID channel.ID) {
	r.mu.Lock()
	if _, coordinated := r.ready[chID]; coordinated {
		r.mu.Unlock()
		return
	}

	r.received[chID]++
	waiters := r.waitersIfReadyLocked(chID)
	r.mu.Unlock()

	r.releaseWaiters(waiters)
}

// NotifyCoordinatedFromLedger records one coordinated event for a specific
// source ledger and ignores duplicates from the same ledger.
func (r *CoordinationRegistry) NotifyCoordinatedFromLedger(chID channel.ID, ledger LedgerBackendKey) {
	r.mu.Lock()
	if _, coordinated := r.ready[chID]; coordinated {
		r.mu.Unlock()
		return
	}

	if _, ok := r.seen[chID]; !ok {
		r.seen[chID] = make(map[LedgerBackendKey]struct{})
	}

	if _, duplicate := r.seen[chID][ledger]; duplicate {
		r.mu.Unlock()
		return
	}

	r.seen[chID][ledger] = struct{}{}
	r.received[chID]++

	waiters := r.waitersIfReadyLocked(chID)
	r.mu.Unlock()

	r.releaseWaiters(waiters)
}

func (r *CoordinationRegistry) waitersIfReadyLocked(chID channel.ID) []*coordinationWaiter {

	expected, ok := r.expected[chID]
	if !ok || r.received[chID] < expected {
		return nil
	}

	return r.markReadyLocked(chID)
}

func (r *CoordinationRegistry) markReadyLocked(chID channel.ID) []*coordinationWaiter {
	waiters := r.waiters[chID]
	delete(r.waiters, chID)
	r.ready[chID] = struct{}{}
	delete(r.expected, chID)
	delete(r.received, chID)
	delete(r.seen, chID)
	return waiters
}

func (r *CoordinationRegistry) releaseWaiters(waiters []*coordinationWaiter) {
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
