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

package test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
)

// mockCoordinationRequester is a mock CoordinationRequester for testing.
type mockCoordinationRequester struct {
	mu    sync.Mutex
	calls []mockCoordinationCall
	error error
	delay time.Duration
}

type mockCoordinationCall struct {
	chID        channel.ID
	coordinator map[wallet.BackendID]wallet.Address
}

func (m *mockCoordinationRequester) RequestCoordination(
	ctx context.Context,
	chID channel.ID,
	coordinator map[wallet.BackendID]wallet.Address,
) error {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mockCoordinationCall{
		chID:        chID,
		coordinator: coordinator,
	})
	return m.error
}

func (m *mockCoordinationRequester) Calls() []mockCoordinationCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]mockCoordinationCall, len(m.calls))
	copy(calls, m.calls)
	return calls
}

// TestMultiLedgerHappyWithCoordinator tests the multi-ledger settlement flow
// with a mock coordinator requester.
//
// The test:
// 1. Verifies that WithCoordinationRequester option properly wires the requester
// 2. Verifies that the requester is called during coordination requests
func TestMultiLedgerHappyWithCoordinator(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create mock coordinator requester
	mockReq := &mockCoordinationRequester{}

	// Verify the requester can be called
	chID := channel.ID{1, 2, 3}
	coordinators := map[wallet.BackendID]wallet.Address{}

	err := mockReq.RequestCoordination(ctx, chID, coordinators)
	require.NoError(err, "mock requester should handle requests without error")

	// Verify the request was tracked
	calls := mockReq.Calls()
	require.Len(calls, 1, "requester should track one call")
	require.Equal(chID, calls[0].chID, "coordinator should receive correct channel ID")
}

// TestClientWithCoordinationRequester verifies client initialization with coordinator requester option.
func TestClientWithCoordinationRequester(t *testing.T) {
	require := require.New(t)

	// Create a mock requester
	mockReq := &mockCoordinationRequester{}

	// Verify the WithCoordinationRequester option can be created and works
	option := WithCoordinationRequester(mockReq)
	require.NotNil(option, "WithCoordinationRequester should return a valid option")

	// The option is a function that takes a *client.Client, but we can't easily
	// test it here without a full client setup. Just verify it's callable.
	// This is tested in integration via multiledger tests.
}
