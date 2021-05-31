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

package subscription_test

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	"perun.network/go-perun/backend/ethereum/bindings/peruntoken"
	"perun.network/go-perun/backend/ethereum/channel/test"
	"perun.network/go-perun/backend/ethereum/subscription"
	"perun.network/go-perun/log"
	pctx "perun.network/go-perun/pkg/context"
	pkgtest "perun.network/go-perun/pkg/test"
)

var event = func() *subscription.Event {
	return &subscription.Event{
		Name: "Approval",
		Data: new(peruntoken.PerunTokenApproval),
	}
}

// TestResistantEventSub_FinalityDepth tests that a TX is confirmed exactly
// after `finalityDepth` blocks.
func TestResistantEventSub_FinalityDepth(t *testing.T) {
	return
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rng := pkgtest.Prng(t)
	require := require.New(t)
	s := test.NewTokenSetup(ctx, t, rng)
	// Create a generic event sub.
	rawSub, err := subscription.NewEventSub(ctx, s.CB, s.Contract, event, 10)
	require.NoError(err)
	defer rawSub.Close()

	for i := 0; i < 10; i++ {
		finality := rng.Int31n(10) + 2
		sub, err := subscription.NewResistantEventSub(ctx, rawSub, s.CB.ContractInterface, uint64(finality))
		require.NoError(err)
		log.Debugf("Finality depth: %d", finality)
		// Send and Confirm the TX
		s.ConfirmTx(s.IncAllowance(ctx), true)
		// Wait `finality-1` blocks
		for j := int32(0); j < finality-1; j++ {
			NEvents(require, 0, sub)
			log.Debug("Commit")
			s.SB.Commit()
		}
		log.Debug("Commit")
		s.SB.Commit()
		log.Debug("Waiting for confirm")
		NEvents(require, 1, sub)
		sub.Close()
	}
}

// TestResistantEventSub_FinalityDepth tests that a TX is confirmed exactly
// after `finalityDepth` blocks even when reorgs occur.
func TestResistantEventSub_Reorg(t *testing.T) {
	return
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rng := pkgtest.Prng(t)
	require := require.New(t)
	s := test.NewTokenSetup(ctx, t, rng)
	// Create a raw event sub.

	for i := 0; i < 10; i++ {
		rawSub, err := subscription.NewEventSub(ctx, s.CB, s.Contract, event, 0)
		require.NoError(err)

		finality := 100
		sub, err := subscription.NewResistantEventSub(ctx, rawSub, s.CB.ContractInterface, uint64(finality))
		require.NoError(err)
		// Send and Confirm the TX
		s.ConfirmTx(s.IncAllowance(ctx), true)
		// Reorg until the block hight hits `finality` blocks.
		for h := int64(0); h < int64(finality); {
			d := rng.Int63n(10) + 1
			NoEvent(require, sub)
			log.Debugf("[h=%d] Reorg with depth: %d", h, d)
			s.SB.Reorg(ctx, 1, d+1, func(txs []types.Transactions) []types.Transactions {
				return txs
			})
			h += d
		}
		log.Debug("done")
		// Verify that the event arrived.
		OneEvent(require, sub)
		rawSub.Close()
		sub.Close()
	}
}

func TestResistantEventSub_New(t *testing.T) {
	return //TODO
	require.PanicsWithValue(t, "finalityDepth needs to be at least 2", func() {
		subscription.NewResistantEventSub(context.Background(), nil, nil, 1)
	})
}

func NoEvent(require *require.Assertions, sub *subscription.ResistantEventSub) {
	NEvents(require, 0, sub)
}

func OneEvent(require *require.Assertions, sub *subscription.ResistantEventSub) {
	NEvents(require, 1, sub)
}

func NEvents(require *require.Assertions, n int, sub *subscription.ResistantEventSub) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	sink := make(chan *subscription.Event, n+1)

	err := sub.ReadAll(ctx, sink)
	require.True(pctx.IsContextError(err))

	for i := 0; i < n; i++ {
		require.NotNil(<-sink)
	}

	select {
	case event := <-sink:
		require.Nil(event)
	default:
	}
}
