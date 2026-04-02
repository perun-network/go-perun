// Copyright 2025 - See NOTICE file for copyright holders.
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
	"math/big"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"perun.network/go-perun/wallet"
	wtest "perun.network/go-perun/wallet/test"
	pkgtest "polycry.pt/poly-go/test"
)

type testAsset struct {
	id byte
}

func (a *testAsset) MarshalBinary() ([]byte, error) { return []byte{a.id}, nil }
func (a *testAsset) UnmarshalBinary(data []byte) error {
	if len(data) > 0 {
		a.id = data[0]
	}
	return nil
}
func (a *testAsset) Equal(b Asset) bool {
	other, ok := b.(*testAsset)
	return ok && a.id == other.id
}
func (a *testAsset) Address() []byte { return []byte{a.id} }

type testLedgerIDMapKey string

type testLedgerID string

func (id testLedgerID) MapKey() testLedgerIDMapKey { return testLedgerIDMapKey(id) }

type testLedgerBackendID struct {
	backendID uint32
	ledgerID  testLedgerID
}

func (id testLedgerBackendID) BackendID() uint32 { return id.backendID }
func (id testLedgerBackendID) LedgerID() testLedgerID {
	return id.ledgerID
}

type testMultiLedgerAsset struct {
	testAsset
	ledgerID testLedgerBackendID
}

func (a *testMultiLedgerAsset) Equal(b Asset) bool {
	other, ok := b.(*testMultiLedgerAsset)
	return ok && a.id == other.id && a.ledgerID.ledgerID == other.ledgerID.ledgerID
}

func (a *testMultiLedgerAsset) LedgerBackendID() testLedgerBackendID {
	return a.ledgerID
}

func newStateMachineForPhase2(t *testing.T, rng *rand.Rand, phase Phase, assets []Asset, withCoordinator bool) *StateMachine {
	t.Helper()

	accs, parts := wtest.NewRandomAccounts(rng, 2, TestBackendID)
	nonce := NonceFromBytes([]byte{1, 2, 3})
	var coordinator map[wallet.BackendID]wallet.Address
	if withCoordinator {
		coordinator = map[wallet.BackendID]wallet.Address{TestBackendID: parts[0][TestBackendID]}
	}
	params := *NewParamsUnsafe(60, parts, NoApp(), nonce, true, false, ZeroAux, coordinator)

	sm, err := NewStateMachine(accs[0], params)
	require.NoError(t, err)

	backends := make([]wallet.BackendID, len(assets))
	for i := range backends {
		backends[i] = TestBackendID
	}

	state := &State{
		ID:      params.ID(),
		Version: 1,
		App:     params.App,
		Allocation: Allocation{
			Backends: backends,
			Assets:   assets,
			Balances: MakeBalances(len(assets), len(params.Parts)),
		},
		Data:    NoData(),
		IsFinal: true,
	}

	// Give one non-zero balance to avoid accidental zero-sum edge cases.
	state.Balances[0][0] = big.NewInt(1)

	sm.machine.currentTX = Transaction{State: state, Sigs: make([]wallet.Sig, len(params.Parts))}
	sm.machine.phase = phase
	return sm
}

func TestSetCoordinated_MultiLedgerTransitions(t *testing.T) {
	rng := pkgtest.Prng(t)
	assets := []Asset{
		&testMultiLedgerAsset{testAsset: testAsset{id: 1}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-a"}},
		&testMultiLedgerAsset{testAsset: testAsset{id: 2}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-b"}},
	}

	for _, phase := range []Phase{Registered, Progressed} {
		sm := newStateMachineForPhase2(t, rng, phase, assets, true)
		require.NoError(t, sm.SetCoordinated())
		require.Equal(t, Coordinated, sm.Phase())
	}
}

func TestSetCoordinated_Idempotent(t *testing.T) {
	rng := pkgtest.Prng(t)
	assets := []Asset{
		&testMultiLedgerAsset{testAsset: testAsset{id: 1}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-a"}},
		&testMultiLedgerAsset{testAsset: testAsset{id: 2}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-b"}},
	}

	sm := newStateMachineForPhase2(t, rng, Coordinated, assets, true)
	require.NoError(t, sm.SetCoordinated())
	require.Equal(t, Coordinated, sm.Phase())
}

func TestSetCoordinated_MultiLedgerWithoutCoordinatorFails(t *testing.T) {
	rng := pkgtest.Prng(t)
	assets := []Asset{
		&testMultiLedgerAsset{testAsset: testAsset{id: 1}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-a"}},
		&testMultiLedgerAsset{testAsset: testAsset{id: 2}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-b"}},
	}

	sm := newStateMachineForPhase2(t, rng, Registered, assets, false)
	err := sm.SetCoordinated()
	require.Error(t, err)
	require.True(t, IsPhaseTransitionError(err))
	require.Equal(t, Registered, sm.Phase())
}

func TestSetCoordinated_SingleLedgerFails(t *testing.T) {
	rng := pkgtest.Prng(t)
	assets := []Asset{
		&testAsset{id: 1},
		&testAsset{id: 2},
	}

	sm := newStateMachineForPhase2(t, rng, Registered, assets, true)
	err := sm.SetCoordinated()
	require.Error(t, err)
	require.True(t, IsPhaseTransitionError(err))
	require.Equal(t, Registered, sm.Phase())
}

func TestSetWithdrawing_MultiLedgerRequiresCoordinatedWhenCoordinatorSet(t *testing.T) {
	rng := pkgtest.Prng(t)
	assets := []Asset{
		&testMultiLedgerAsset{testAsset: testAsset{id: 1}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-a"}},
		&testMultiLedgerAsset{testAsset: testAsset{id: 2}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-b"}},
	}

	sm := newStateMachineForPhase2(t, rng, Registered, assets, true)
	err := sm.SetWithdrawing()
	require.Error(t, err)
	require.True(t, IsPhaseTransitionError(err))
	require.Equal(t, Registered, sm.Phase())

	sm = newStateMachineForPhase2(t, rng, Coordinated, assets, true)
	require.NoError(t, sm.SetWithdrawing())
	require.Equal(t, Withdrawing, sm.Phase())
}

func TestSetWithdrawing_MultiLedgerWithoutCoordinatorUnchanged(t *testing.T) {
	rng := pkgtest.Prng(t)
	assets := []Asset{
		&testMultiLedgerAsset{testAsset: testAsset{id: 1}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-a"}},
		&testMultiLedgerAsset{testAsset: testAsset{id: 2}, ledgerID: testLedgerBackendID{backendID: 1, ledgerID: "ledger-b"}},
	}

	for _, phase := range []Phase{Final, Registered, Progressed, Withdrawing} {
		sm := newStateMachineForPhase2(t, rng, phase, assets, false)
		require.NoError(t, sm.SetWithdrawing())
		require.Equal(t, Withdrawing, sm.Phase())
	}
}

func TestSetWithdrawing_SingleLedgerUnchanged(t *testing.T) {
	rng := pkgtest.Prng(t)
	assets := []Asset{
		&testAsset{id: 1},
		&testAsset{id: 2},
	}

	for _, phase := range []Phase{Final, Registered, Progressed, Withdrawing} {
		sm := newStateMachineForPhase2(t, rng, phase, assets, false)
		require.NoError(t, sm.SetWithdrawing())
		require.Equal(t, Withdrawing, sm.Phase())
	}
}
