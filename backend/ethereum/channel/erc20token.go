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

package channel

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"perun.network/go-perun/backend/ethereum/bindings/peruntoken"
)

type ERC20Token struct {
	CB bind.ContractBackend
	common.Address
}

func (e *ERC20Token) BalanceOf(ctx context.Context, address common.Address) (*big.Int, error) {
	token, err := peruntoken.NewERC20(e.Address, e.CB)
	if err != nil {
		return nil, err
	}
	opts := &bind.CallOpts{Context: ctx, BlockNumber: nil}

	balance, err := token.BalanceOf(opts, address)

	if err != nil {
		return nil, err
	}

	return balance, nil
}
