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

package multi //nolint:testpackage // Test exercises package-internal coordination behavior.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
)

func TestCoordinationRegistry_RequestAwaitNotify(t *testing.T) {
	r := NewCoordinationRegistry()
	chID := channel.ID{1}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	require.NoError(t, r.RequestCoordination(ctx, chID, nil))

	done := make(chan error, 1)

	go func() {
		done <- r.AwaitCoordinated(ctx, chID)
	}()

	r.NotifyCoordinated(chID)
	require.NoError(t, <-done)
}

func TestCoordinationRegistry_AwaitWithoutRequest(t *testing.T) {
	r := NewCoordinationRegistry()
	chID := channel.ID{2}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- r.AwaitCoordinated(ctx, chID)
	}()

	r.NotifyCoordinated(chID)
	require.NoError(t, <-done)
}

func TestCoordinationRegistry_AwaitCanceled(t *testing.T) {
	r := NewCoordinationRegistry()
	chID := channel.ID{3}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)

	go func() {
		done <- r.AwaitCoordinated(ctx, chID)
	}()

	cancel()

	err := <-done
	require.ErrorIs(t, err, context.Canceled)

	// Must stay safe if a delayed notification arrives after cancellation.
	r.NotifyCoordinated(chID)
}

func TestCoordinationRegistry_ConcurrentAwaiters(t *testing.T) {
	r := NewCoordinationRegistry()
	chID := channel.ID{4}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	const n = 16
	for range n {
		require.NoError(t, r.RequestCoordination(ctx, chID, nil))
	}

	errs := make(chan error, n)

	var wg sync.WaitGroup
	wg.Add(n)

	for range n {
		go func() {
			defer wg.Done()

			errs <- r.AwaitCoordinated(ctx, chID)
		}()
	}

	r.NotifyCoordinated(chID)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
}

func TestCoordinationRegistry_RequesterCalled(t *testing.T) {
	r := NewCoordinationRegistry()
	requester := &mockCoordinationRequester{}
	r.SetRequester(requester)

	chID := channel.ID{5}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	coordinator := map[wallet.BackendID]wallet.Address{}
	require.NoError(t, r.RequestCoordination(ctx, chID, coordinator))
	require.True(t, requester.called)
	require.Equal(t, chID, requester.lastID)
	require.Equal(t, coordinator, requester.lastCoordinator)
}

func TestCoordinationRegistry_RequesterError(t *testing.T) {
	r := NewCoordinationRegistry()
	r.SetRequester(&mockCoordinationRequester{err: errors.New("request failed")})

	chID := channel.ID{6}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := r.RequestCoordination(ctx, chID, nil)
	require.EqualError(t, err, "request failed")
}

type mockCoordinationRequester struct {
	called          bool
	lastID          channel.ID
	lastCoordinator map[wallet.BackendID]wallet.Address
	err             error
}

func (m *mockCoordinationRequester) RequestCoordination(_ context.Context, chID channel.ID, coordinator map[wallet.BackendID]wallet.Address) error {
	m.called = true
	m.lastID = chID
	m.lastCoordinator = coordinator

	return m.err
}
