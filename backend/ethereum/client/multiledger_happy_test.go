// Copyright 2019 - See NOTICE file for copyright holders.
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

package client_test

import (
	"context"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	ethchannel "perun.network/go-perun/backend/ethereum/channel"
	chtest "perun.network/go-perun/backend/ethereum/channel/test"
	"perun.network/go-perun/backend/ethereum/wallet/keystore"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/client"
	wtest "perun.network/go-perun/wallet/test"
	"perun.network/go-perun/watcher/local"
	"perun.network/go-perun/wire"
	"polycry.pt/poly-go/test"
)

type multiLedgerTest struct {
	c1, c2 testClient
	l1, l2 testLedger
}

const (
	challengeDuration = 10
	testDuration      = 10 * time.Second
	txFinalityDepth   = 1
	blockInterval     = 100 * time.Millisecond
)

func TestMultiLedgerHappy(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	mlt := setupMultiLedgerTest(ctx, t)
	alice, bob := mlt.c1, mlt.c2

	// Define initial balances.
	initBals := channel.Balances{
		{big.NewInt(10), big.NewInt(0)}, // Asset 1.
		{big.NewInt(0), big.NewInt(10)}, // Asset 2.
	}
	updateBals1 := channel.Balances{
		{big.NewInt(5), big.NewInt(5)}, // Asset 1.
		{big.NewInt(3), big.NewInt(7)}, // Asset 2.
	}
	updateBals2 := channel.Balances{
		{big.NewInt(1), big.NewInt(9)}, // Asset 1.
		{big.NewInt(5), big.NewInt(5)}, // Asset 2.
	}

	// Establish ledger channel between Alice and Bob.

	// Create channel proposal.
	parts := []wire.Address{alice.wireAddress, bob.wireAddress}
	initAlloc := channel.NewAllocation(len(parts), &mlt.l1.asset, &mlt.l2.asset)
	initAlloc.Balances = initBals
	prop, err := client.NewLedgerChannelProposal(
		challengeDuration,
		alice.wireAddress,
		initAlloc,
		parts,
	)
	require.NoError(err, "creating ledger channel proposal")

	// Setup proposal handler.
	channels := make(chan *client.Channel, 1)
	errs := make(chan error)
	var channelHandler client.ProposalHandlerFunc = func(cp client.ChannelProposal, pr *client.ProposalResponder) {
		switch cp := cp.(type) {
		case *client.LedgerChannelProposal:
			ch, err := pr.Accept(ctx, cp.Accept(bob.wireAddress, client.WithRandomNonce()))
			if err != nil {
				errs <- errors.WithMessage(err, "accepting ledger channel proposal")
				return
			}
			channels <- ch
		default:
			errs <- errors.Errorf("invalid channel proposal: %v", cp)
			return
		}
	}
	var updateHandler client.UpdateHandlerFunc = func(
		s *channel.State, cu client.ChannelUpdate, ur *client.UpdateResponder,
	) {
		err := ur.Accept(ctx)
		if err != nil {
			errs <- errors.WithMessage(err, "Bob: accepting channel update")
		}
	}
	go alice.Handle(channelHandler, updateHandler)
	go bob.Handle(channelHandler, updateHandler)

	// Open channel.
	chAliceBob, err := alice.ProposeChannel(ctx, prop)
	require.NoError(err, "opening channel between Alice and Ingrid")
	var chBobAlice *client.Channel
	select {
	case chBobAlice = <-channels:
	case err := <-errs:
		t.Fatalf("Error in go-routine: %v", err)
	}

	// Update channel.
	err = chAliceBob.Update(ctx, func(s *channel.State) error {
		s.Balances = updateBals1
		return nil
	})
	require.NoError(err)

	chBobAlice.Update(ctx, func(s *channel.State) error {
		s.Balances = updateBals2
		return nil
	})
	require.NoError(err)

	err = chAliceBob.Update(ctx, func(s *channel.State) error {
		s.IsFinal = true
		return nil
	})
	require.NoError(err)

	// Close channel.
	err = chAliceBob.Settle(ctx, false)
	require.NoError(err)
	err = chBobAlice.Settle(ctx, false)
	require.NoError(err)
}

func setupMultiLedgerTest(ctx context.Context, t *testing.T) (mlt multiLedgerTest) {
	t.Helper()
	rng := test.Prng(t)

	// Setup ledgers.
	l1 := setupLedger(ctx, t, rng, big.NewInt(1337))
	l2 := setupLedger(ctx, t, rng, big.NewInt(1338))

	// Setup message bus.
	bus := wire.NewLocalBus()

	// Setup clients.
	c1 := setupClient(ctx, t, rng, l1, l2, bus)
	c2 := setupClient(ctx, t, rng, l1, l2, bus)

	// Fund accounts.
	l1.simSetup.SimBackend.FundAddress(ctx, c1.accountAddress)
	l1.simSetup.SimBackend.FundAddress(ctx, c2.accountAddress)
	l2.simSetup.SimBackend.FundAddress(ctx, c1.accountAddress)
	l2.simSetup.SimBackend.FundAddress(ctx, c2.accountAddress)

	return multiLedgerTest{
		c1: c1,
		c2: c2,
		l1: l1,
		l2: l2,
	}
}

type testLedger struct {
	simSetup    *chtest.SimSetup
	adjudicator common.Address
	assetHolder common.Address
	asset       ethchannel.Asset
}

func setupLedger(ctx context.Context, t *testing.T, rng *rand.Rand, chainID *big.Int) testLedger {
	t.Helper()

	// Set chainID for SimulatedBackend.
	cfg := *params.AllEthashProtocolChanges
	cfg.ChainID = new(big.Int).Set(chainID)
	params.AllEthashProtocolChanges = &cfg
	simSetup := chtest.NewSimSetup(t, rng, txFinalityDepth, blockInterval)

	adjudicator, err := ethchannel.DeployAdjudicator(ctx, *simSetup.CB, simSetup.TxSender.Account)
	require.NoError(t, err)
	assetHolder, err := ethchannel.DeployETHAssetholder(ctx, *simSetup.CB, adjudicator, simSetup.TxSender.Account)
	require.NoError(t, err)

	asset := ethchannel.NewAssetFromEth(chainID, assetHolder)
	return testLedger{
		simSetup:    simSetup,
		adjudicator: adjudicator,
		assetHolder: assetHolder,
		asset:       *asset,
	}
}

func (l testLedger) ChainID() ethchannel.ChainID {
	return ethchannel.MakeChainID(l.simSetup.SimBackend.Blockchain().Config().ChainID)
}

type testClient struct {
	*client.Client
	accountAddress common.Address
	wireAddress    wire.Address
}

func setupClient(
	ctx context.Context, t *testing.T, rng *rand.Rand,
	l1, l2 testLedger, bus wire.Bus,
) testClient {
	require := require.New(t)
	// Setup wallet and account.
	w := wtest.RandomWallet().(*keystore.Wallet)
	acc := w.NewRandomAccount(rng).(*keystore.Account)

	// Fund account.
	l1.simSetup.SimBackend.FundAddress(ctx, acc.Account.Address)

	// Setup contract backends.
	signer1 := types.NewEIP155Signer(l1.ChainID().Int)
	cb1 := ethchannel.NewContractBackend(
		l1.simSetup.CB,
		keystore.NewTransactor(*w, signer1),
		l1.simSetup.CB.TxFinalityDepth(),
	)
	signer2 := types.NewEIP155Signer(l2.ChainID().Int)
	cb2 := ethchannel.NewContractBackend(
		l2.simSetup.CB,
		keystore.NewTransactor(*w, signer2),
		l2.simSetup.CB.TxFinalityDepth(),
	)

	// Setup funder.
	funder := ethchannel.NewFunder()
	funder.RegisterBackend(l1.ChainID(), &cb1)
	funder.RegisterBackend(l2.ChainID(), &cb2)
	registered := funder.RegisterAsset(l1.asset, ethchannel.NewETHDepositor(), acc.Account)
	require.True(registered)
	registered = funder.RegisterAsset(l2.asset, ethchannel.NewETHDepositor(), acc.Account)
	require.True(registered)

	// Setup adjudicator.
	adj := ethchannel.NewAdjudicator(acc.Account.Address, acc.Account)
	adj.RegisterBackend(l1.ChainID(), l1.simSetup.CB, l1.adjudicator)
	adj.RegisterBackend(l2.ChainID(), l2.simSetup.CB, l2.adjudicator)

	// Setup watcher.
	watcher, err := local.NewWatcher(adj)
	require.NoError(err)

	c, err := client.New(
		acc.Address(),
		bus,
		funder,
		adj,
		w,
		watcher,
	)
	require.NoError(err)

	return testClient{
		Client:         c,
		accountAddress: acc.Account.Address,
		wireAddress:    acc.Address(),
	}
}
