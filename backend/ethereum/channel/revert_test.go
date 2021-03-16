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

package channel_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	"perun.network/go-perun/backend/ethereum/bindings/peruntoken"
	ethchannel "perun.network/go-perun/backend/ethereum/channel"
	"perun.network/go-perun/backend/ethereum/channel/test"
	"perun.network/go-perun/backend/ethereum/wallet/keystore"
	channeltest "perun.network/go-perun/channel/test"
	pkgtest "perun.network/go-perun/pkg/test"
	wallettest "perun.network/go-perun/wallet/test"
)

func TestSimBackend_TxRevert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTxTimeout)
	defer cancel()
	rng := pkgtest.Prng(t)

	// Simulated chain setup.
	sb := test.NewSimulatedBackend()
	ksWallet := wallettest.RandomWallet().(*keystore.Wallet)
	account := &ksWallet.NewRandomAccount(rng).(*keystore.Account).Account
	sb.FundAddress(ctx, account.Address)
	cb := ethchannel.NewContractBackend(sb, keystore.NewTransactor(*ksWallet, types.NewEIP155Signer(big.NewInt(1337))))

	// Setup Perun Token.
	tokenAddr, err := ethchannel.DeployPerunToken(ctx, cb, *account, []common.Address{account.Address}, channeltest.MaxBalance)
	require.NoError(t, err)
	token, err := peruntoken.NewERC20(tokenAddr, cb)
	require.NoError(t, err)

	// Create a snapshot.
	snapshot := sb.Blockchain().CurrentBlock().NumberU64()

	// Send the transaction.
	opts, err := cb.NewTransactor(ctx, txGasLimit, *account)
	require.NoError(t, err)
	tx, err := token.IncreaseAllowance(opts, account.Address, big.NewInt(1))
	require.NoError(t, err)

	// Wait for the TX to be mined.
	_, err = cb.ConfirmTransaction(ctx, tx, *account)
	require.NoError(t, err)
	// Reset the chain to where the TX was not mined.
	require.NoError(t, sb.Blockchain().SetHead(snapshot))
	// Check that the TX is not included anymore (ctx will time out).
	_, err = cb.ConfirmTransaction(ctx, tx, *account)
	require.Error(t, err, "TX should have been reverted")
}

func TestSimBackend_EventRevert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTxTimeout)
	defer cancel()
	rng := pkgtest.Prng(t)

	// Simulated chain setup.
	sb := test.NewSimulatedBackend()
	ksWallet := wallettest.RandomWallet().(*keystore.Wallet)
	account := &ksWallet.NewRandomAccount(rng).(*keystore.Account).Account
	sb.FundAddress(ctx, account.Address)
	cb := ethchannel.NewContractBackend(sb, keystore.NewTransactor(*ksWallet, types.NewEIP155Signer(big.NewInt(1337))))

	// Setup Perun Token.
	tokenAddr, err := ethchannel.DeployPerunToken(ctx, cb, *account, []common.Address{account.Address}, channeltest.MaxBalance)
	require.NoError(t, err)
	token, err := peruntoken.NewERC20(tokenAddr, cb)
	require.NoError(t, err)

	// Create a snapshot.
	snapshot := sb.Blockchain().CurrentBlock().NumberU64()

	wOpts := &bind.WatchOpts{
		Context: ctx,
	}
	sink := make(chan *peruntoken.ERC20Approval, 1)
	sub, err := token.WatchApproval(wOpts, sink, nil, nil)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	sb.Commit()
	// Send the transaction.
	opts, err := cb.NewTransactor(ctx, txGasLimit, *account)
	require.NoError(t, err)
	tx, err := token.IncreaseAllowance(opts, account.Address, big.NewInt(1))
	require.NoError(t, err)

	// Wait for the TX to be mined.
	_, err = cb.ConfirmTransaction(ctx, tx, *account)
	require.NoError(t, err)
	// Wait for Event.
	e := <-sink
	require.NotNil(t, e)
	require.Equal(t, e.Owner, account.Address)

	// Reset the chain to where the TX was not mined.
	require.NoError(t, sb.Blockchain().SetHead(snapshot))
	sb.Blockchain().Reset()
	// Check that the TX is not included anymore (ctx will time out).
	_, err = cb.ConfirmTransaction(ctx, tx, *account)
	require.Error(t, err, "TX should have been reverted")

	// Send the transaction.
	opts, err = cb.NewTransactor(ctx, txGasLimit, *account)
	require.NoError(t, err)
	opts.Nonce = big.NewInt(2)
	tx, err = token.IncreaseAllowance(opts, account.Address, big.NewInt(1))
	require.NoError(t, err)

	// Wait for the TX to be mined.
	_, err = cb.ConfirmTransaction(ctx, tx, *account)
	require.NoError(t, err)
	// Wait for Event.
	e = <-sink
	require.NotNil(t, e)
	require.Equal(t, e.Owner, account.Address)
}
