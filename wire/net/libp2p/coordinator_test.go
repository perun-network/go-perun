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

package libp2p_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
	wtest "perun.network/go-perun/wallet/test"
	"perun.network/go-perun/wire/net/libp2p"
	pkgtest "polycry.pt/poly-go/test"
)

func TestRelayCoordinationRequester_RequestCoordination(t *testing.T) {
	requester, coordinatorAddr, coordinatorAcc := setupCoordinationRequesterPair(t)
	defer coordinatorAcc.RemoveRequestCoordinationHandler()

	expectedID := channel.ID{1, 2, 3}
	requests := make(chan libp2p.RequestCoordinationBody, 1)
	coordinatorAcc.SetRequestCoordinationHandler(func(_ context.Context, req libp2p.RequestCoordinationBody) libp2p.Response {
		requests <- req
		return libp2p.Response{Status: "ok"}
	})

	coordinationCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, requester.RequestCoordination(
		coordinationCtx,
		expectedID,
		map[wallet.BackendID]wallet.Address{channel.TestBackendID: coordinatorAddr},
	))

	select {
	case got := <-requests:
		require.Equal(t, "0102030000000000000000000000000000000000000000000000000000000000", got.ChannelID)
		require.NotEmpty(t, got.RequestID)
		require.NotNil(t, got.Params)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coordination request")
	}
}

func TestRelayCoordinationRequester_RequestCoordinationRejected(t *testing.T) {
	requester, coordinatorAddr, coordinatorAcc := setupCoordinationRequesterPair(t)
	defer coordinatorAcc.RemoveRequestCoordinationHandler()

	coordinatorAcc.SetRequestCoordinationHandler(func(context.Context, libp2p.RequestCoordinationBody) libp2p.Response {
		return libp2p.Response{Status: "error", Reason: "malformed body"}
	})

	coordinationCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := requester.RequestCoordination(
		coordinationCtx,
		channel.ID{7, 8, 9},
		map[wallet.BackendID]wallet.Address{channel.TestBackendID: coordinatorAddr},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed body")
}

func TestRelayCoordinationRequester_RequestCoordinationDuplicateAccepted(t *testing.T) {
	requester, coordinatorAddr, coordinatorAcc := setupCoordinationRequesterPair(t)
	defer coordinatorAcc.RemoveRequestCoordinationHandler()

	coordinatorAcc.SetRequestCoordinationHandler(func(context.Context, libp2p.RequestCoordinationBody) libp2p.Response {
		return libp2p.Response{Status: "duplicate"}
	})

	coordinationCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := requester.RequestCoordination(
		coordinationCtx,
		channel.ID{4, 5, 6},
		map[wallet.BackendID]wallet.Address{channel.TestBackendID: coordinatorAddr},
	)
	require.NoError(t, err)
}

func TestRelayCoordinationRequester_RequestCoordinationMissingCoordinator(t *testing.T) {
	rng := pkgtest.Prng(t)
	requesterAcc := libp2p.NewRandomAccount(rng)
	defer func() {
		require.NoError(t, requesterAcc.Close())
	}()

	requester := libp2p.NewRelayCoordinationRequester(requesterAcc)
	err := requester.RequestCoordination(context.Background(), channel.ID{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing coordinator address")
}

func TestRelayCoordinationRequester_RequestWitness(t *testing.T) {
	requester, coordinatorAddr, coordinatorAcc := setupCoordinationRequesterPair(t)
	defer coordinatorAcc.RemoveRequestWitnessHandler()

	received := make(chan libp2p.RequestWitnessBody, 1)
	coordinatorAcc.SetRequestWitnessHandler(func(_ context.Context, req libp2p.RequestWitnessBody) libp2p.Response {
		received <- req
		return libp2p.Response{Status: "ok"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := requester.RequestWitness(
		ctx,
		map[wallet.BackendID]wallet.Address{channel.TestBackendID: coordinatorAddr},
		libp2p.RequestWitnessBody{
			ChannelID: "abc123",
			LedgerID:  3,
			State:     json.RawMessage(`{"v":7}`),
			Sigs:      []string{"Zm9v"},
			Source:    "test",
		},
	)
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Status)

	select {
	case req := <-received:
		require.Equal(t, "abc123", req.ChannelID)
		require.Equal(t, 3, req.LedgerID)
		require.Equal(t, "test", req.Source)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for witness request")
	}
}

func TestRelayCoordinationRequester_GetStatus(t *testing.T) {
	requester, coordinatorAddr, coordinatorAcc := setupCoordinationRequesterPair(t)
	defer coordinatorAcc.RemoveGetStatusHandler()

	coordinatorAcc.SetGetStatusHandler(func(_ context.Context, req libp2p.GetStatusQuery) libp2p.StatusResponse {
		require.Equal(t, "0909090000000000000000000000000000000000000000000000000000000000", req.ChannelID)
		return libp2p.StatusResponse{Status: "decided", Decision: json.RawMessage(`{"version":9}`)}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := requester.GetStatus(
		ctx,
		map[wallet.BackendID]wallet.Address{channel.TestBackendID: coordinatorAddr},
		channel.ID{9, 9, 9},
	)
	require.NoError(t, err)
	require.Equal(t, "decided", resp.Status)
	require.Equal(t, `{"version":9}`, string(resp.Decision))
}

func setupCoordinationRequesterPair(t *testing.T) (*libp2p.RelayCoordinationRequester, wallet.Address, *libp2p.Account) {
	t.Helper()

	rng := pkgtest.Prng(t)
	requesterAcc := libp2p.NewRandomAccount(rng)
	t.Cleanup(func() {
		require.NoError(t, requesterAcc.Close())
	})

	coordinatorAcc := libp2p.NewRandomAccount(rng)
	t.Cleanup(func() {
		require.NoError(t, coordinatorAcc.Close())
	})

	onChainCoordinatorAddr := wtest.NewRandomAddress(rng, channel.TestBackendID)
	require.NoError(t, coordinatorAcc.RegisterOnChainAddress(onChainCoordinatorAddr))

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	require.NoError(t, waitForRelayAddressBookEntry(waitCtx, requesterAcc, onChainCoordinatorAddr))

	return libp2p.NewRelayCoordinationRequester(requesterAcc), onChainCoordinatorAddr, coordinatorAcc
}

func waitForRelayAddressBookEntry(ctx context.Context, acc *libp2p.Account, onChainAddr wallet.Address) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		_, err := acc.QueryOnChainAddress(onChainAddr)
		if err == nil {
			return nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return lastErr
		case <-ticker.C:
		}
	}
}
