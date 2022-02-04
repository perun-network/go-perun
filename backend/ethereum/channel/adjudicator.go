// Copyright 2020 - See NOTICE file for copyright holders.
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
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/pkg/errors"

	"perun.network/go-perun/backend/ethereum/bindings"
	"perun.network/go-perun/backend/ethereum/bindings/adjudicator"
	cherrors "perun.network/go-perun/backend/ethereum/channel/errors"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/client"
	"perun.network/go-perun/log"
	psync "polycry.pt/poly-go/sync"
)

// compile time check that we implement the perun adjudicator interface.
var _ channel.Adjudicator = (*Adjudicator)(nil)

// The Adjudicator struct implements the channel.Adjudicator interface
// It provides all functionality to close a channel.
type Adjudicator struct {
	// The address to which we send all funds.
	Receiver common.Address
	// Structured logger
	log log.Logger
	// txSender is sending the TX.
	txSender accounts.Account
	// backends is the backend registry
	backends map[ChainIDMapKey]adjudicatorBackend
}

type adjudicatorBackend struct {
	backend  *ContractBackend
	contract *adjudicator.Adjudicator
	bound    *bind.BoundContract
	mu       *psync.Mutex
	txSender accounts.Account
}

// NewAdjudicator creates a new ethereum adjudicator. The receiver is the
// on-chain address that receives withdrawals.
func NewAdjudicator(receiver common.Address, txSender accounts.Account) *Adjudicator {
	return &Adjudicator{
		Receiver: receiver,
		txSender: txSender,
		log:      log.WithField("txSender", txSender.Address),
		backends: make(map[ChainIDMapKey]adjudicatorBackend),
	}
}

// RegisterBackend registers a contract backend for a chain ID.
func (a *Adjudicator) RegisterBackend(id ChainID, cb *ContractBackend, contract common.Address) {
	adj, err := adjudicator.NewAdjudicator(contract, cb)
	if err != nil {
		panic("Could not create a new instance of adjudicator")
	}
	boundContract := bind.NewBoundContract(contract, bindings.ABI.Adjudicator, cb, cb, cb)

	a.backends[id.MapKey()] = adjudicatorBackend{
		backend:  cb,
		contract: adj,
		bound:    boundContract,
		mu:       &psync.Mutex{},
		txSender: a.txSender,
	}
}

// Progress progresses a channel state on-chain.
func (a *Adjudicator) Progress(ctx context.Context, req channel.ProgressReq) error {
	ethNewState := ToEthState(req.NewState)
	ethActorIndex := big.NewInt(int64(req.Idx))

	progress := func(
		contract *adjudicator.Adjudicator,
		opts *bind.TransactOpts,
		params adjudicator.ChannelParams,
		state adjudicator.ChannelState,
		_ [][]byte,
	) (*types.Transaction, error) {
		return contract.Progress(opts, params, state, ethNewState, ethActorIndex, req.Sig)
	}

	backends := makeBackendSet(a)
	err := backends.Add(req.AdjudicatorReq.Tx.Assets, req.NewState.Assets)
	if err != nil {
		return err
	}

	return a.call(ctx, backends.List(), req.AdjudicatorReq, progress, Progress)
}

type backendSet struct {
	a *Adjudicator
	m map[ChainIDMapKey]adjudicatorBackend
}

func makeBackendSet(a *Adjudicator) backendSet {
	return backendSet{
		a: a,
		m: make(map[ChainIDMapKey]adjudicatorBackend),
	}
}

func (s backendSet) Add(assets ...[]channel.Asset) error {
	for _, assetList := range assets {
		for _, asset := range assetList {
			ethAsset := asset.(*Asset)
			b, ok := s.a.backends[ethAsset.ChainID.MapKey()]
			if !ok {
				return errors.Errorf("no backend registered for chain ID: %v", ethAsset.ChainID)
			}
			s.m[ethAsset.ChainID.MapKey()] = b
		}
	}
	return nil
}

func (s backendSet) List() []adjudicatorBackend {
	backends := make([]adjudicatorBackend, len(s.m))
	i := 0
	for _, b := range s.m {
		backends[i] = b
		i++
	}
	return backends
}

func (a *Adjudicator) callRegister(ctx context.Context, req channel.AdjudicatorReq, subChannels []channel.SignedState) error {
	register := func(
		contract *adjudicator.Adjudicator,
		opts *bind.TransactOpts,
		params adjudicator.ChannelParams,
		state adjudicator.ChannelState,
		sigs [][]byte,
	) (*types.Transaction, error) {
		ch := adjudicator.AdjudicatorSignedState{
			Params: params,
			State:  state,
			Sigs:   sigs,
		}
		sub := toEthSignedStates(subChannels)
		return contract.Register(opts, ch, sub)
	}

	// Gather backends for channel and subchannels.
	backends := makeBackendSet(a)
	err := backends.Add(req.Tx.Assets)
	if err != nil {
		return err
	}
	for _, sub := range subChannels {
		err := backends.Add(sub.State.Assets)
		if err != nil {
			return err
		}
	}

	return a.call(ctx, backends.List(), req, register, Register)
}

func toEthSignedStates(subChannels []channel.SignedState) (ethSubChannels []adjudicator.AdjudicatorSignedState) {
	ethSubChannels = make([]adjudicator.AdjudicatorSignedState, len(subChannels))
	for i, x := range subChannels {
		ethSubChannels[i] = adjudicator.AdjudicatorSignedState{
			Params: ToEthParams(x.Params),
			State:  ToEthState(x.State),
			Sigs:   x.Sigs,
		}
	}
	return
}

func (a *Adjudicator) callConcludeBackend(ctx context.Context, b adjudicatorBackend, req channel.AdjudicatorReq, subStates channel.StateMap) error {
	ethSubStates := toEthSubStates(req.Tx.State, subStates)

	conclude := func(
		contract *adjudicator.Adjudicator,
		opts *bind.TransactOpts,
		params adjudicator.ChannelParams,
		state adjudicator.ChannelState,
		_ [][]byte,
	) (*types.Transaction, error) {
		return contract.Conclude(opts, params, state, ethSubStates)
	}

	backends := []adjudicatorBackend{b}
	return a.call(ctx, backends, req, conclude, Conclude)
}

func (a *Adjudicator) callConcludeFinalBackend(ctx context.Context, b adjudicatorBackend, req channel.AdjudicatorReq) error {
	concludeFinal := func(
		contract *adjudicator.Adjudicator,
		opts *bind.TransactOpts,
		params adjudicator.ChannelParams,
		state adjudicator.ChannelState,
		sigs [][]byte,
	) (*types.Transaction, error) {
		return contract.ConcludeFinal(opts, params, state, sigs)
	}

	backends := []adjudicatorBackend{b}
	return a.call(ctx, backends, req, concludeFinal, ConcludeFinal)
}

type adjFunc = func(
	contract *adjudicator.Adjudicator,
	opts *bind.TransactOpts,
	params adjudicator.ChannelParams,
	state adjudicator.ChannelState,
	sigs [][]byte,
) (*types.Transaction, error)

// call calls the given contract function `fn` with the data from `req`.
// `fn` should be a method of `a.contract`, like `a.contract.Register`.
// `txType` should be one of the valid transaction types defined in the client package.
func (*Adjudicator) call(ctx context.Context, backends []adjudicatorBackend, req channel.AdjudicatorReq, fn adjFunc, txType OnChainTxType) error {
	for _, b := range backends {
		ethParams := ToEthParams(req.Params)
		ethState := ToEthState(req.Tx.State)

		tx, err := func() (*types.Transaction, error) {
			if !b.mu.TryLockCtx(ctx) {
				return nil, errors.Wrap(ctx.Err(), "context canceled while acquiring tx lock")
			}
			defer b.mu.Unlock()

			trans, err := b.backend.NewTransactor(ctx, GasLimit, b.txSender)
			if err != nil {
				return nil, errors.WithMessage(err, "creating transactor")
			}
			tx, err := fn(b.contract, trans, ethParams, ethState, req.Tx.Sigs)
			if err != nil {
				err = cherrors.CheckIsChainNotReachableError(err)
				return nil, errors.WithMessage(err, "calling adjudicator function")
			}
			log.Debugf("Sent transaction %v", tx.Hash().Hex())
			return tx, nil
		}()
		if err != nil {
			return err
		}

		_, err = b.backend.ConfirmTransaction(ctx, tx, b.txSender)
		if errors.Is(err, errTxTimedOut) {
			err = client.NewTxTimedoutError(txType.String(), tx.Hash().Hex(), err.Error())
		} else if err != nil {
			return errors.WithMessage(err, "mining transaction")
		}
	}
	return nil
}

// ValidateAdjudicator checks if the bytecode at given address is correct.
// Returns a ContractBytecodeError if the bytecode at given address is invalid.
// This error can be checked with function IsErrInvalidContractCode.
func ValidateAdjudicator(ctx context.Context,
	backend bind.ContractCaller, adjudicatorAddr common.Address) error {
	return validateContract(ctx, backend, adjudicatorAddr, adjudicator.AdjudicatorBinRuntime)
}

// toEthSubStates generates a channel tree in depth-first order.
func toEthSubStates(state *channel.State, subStates channel.StateMap) (ethSubStates []adjudicator.ChannelState) {
	for _, subAlloc := range state.Locked {
		subState, ok := subStates[subAlloc.ID]
		if !ok {
			log.Panic("sub-state not found")
		}
		ethSubStates = append(ethSubStates, ToEthState(subState))
		if len(subState.Locked) > 0 {
			_subSubStates := toEthSubStates(subState, subStates)
			ethSubStates = append(ethSubStates, _subSubStates...)
		}
	}
	return
}
