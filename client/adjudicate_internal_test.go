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

package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/channel/multi"
)

func TestChannel_setMachinePhase_CoordinatedEventNonFatal(t *testing.T) {
	ch := testCh()
	err := ch.setMachinePhase(context.Background(), channel.NewCoordinatedEvent(channel.ID{}, &channel.ElapsedTimeout{}, 0))
	require.NoError(t, err)
}

func TestParticipatingLedgers_DeduplicatesByLedgerKey(t *testing.T) {
	assets := []channel.Asset{
		coordinationTestAsset{backendID: 1, ledgerID: "ledger-a"},
		coordinationTestAsset{backendID: 1, ledgerID: "ledger-a"},
		coordinationTestAsset{backendID: 1, ledgerID: "ledger-b"},
	}

	ledgers := participatingLedgers(assets)
	require.Len(t, ledgers, 2)
	require.Equal(t, 2, participatingLedgerCount(assets))
	require.Equal(t, multi.LedgerBackendKey{BackendID: 1, LedgerID: "ledger-a"}, coordinationLedgerBackendKey(ledgers[0]))
	require.Equal(t, multi.LedgerBackendKey{BackendID: 1, LedgerID: "ledger-b"}, coordinationLedgerBackendKey(ledgers[1]))
}

type coordinationTestAsset struct {
	backendID uint32
	ledgerID  coordinationTestLedgerID
}

func (a coordinationTestAsset) MarshalBinary() ([]byte, error) { return []byte{}, nil }

func (a coordinationTestAsset) UnmarshalBinary(_ []byte) error { return nil }

func (a coordinationTestAsset) Equal(other channel.Asset) bool {
	o, ok := other.(coordinationTestAsset)
	return ok && a.backendID == o.backendID && a.ledgerID == o.ledgerID
}

func (a coordinationTestAsset) Address() []byte { return []byte(a.ledgerID) }

func (a coordinationTestAsset) LedgerBackendID() multi.LedgerBackendID {
	return coordinationTestLedgerBackendID{backendID: a.backendID, ledgerID: a.ledgerID}
}

type coordinationTestLedgerID string

func (id coordinationTestLedgerID) MapKey() multi.LedgerIDMapKey { return multi.LedgerIDMapKey(id) }

type coordinationTestLedgerBackendID struct {
	backendID uint32
	ledgerID  coordinationTestLedgerID
}

func (id coordinationTestLedgerBackendID) BackendID() uint32 { return id.backendID }

func (id coordinationTestLedgerBackendID) LedgerID() multi.LedgerID { return id.ledgerID }
