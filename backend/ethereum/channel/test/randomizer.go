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

package test

import (
	"math/big"
	"math/rand"

	"github.com/ethereum/go-ethereum/common"
	ethchannel "perun.network/go-perun/backend/ethereum/channel"
	ethwtest "perun.network/go-perun/backend/ethereum/wallet/test"
	"perun.network/go-perun/channel"
)

type randomizer struct{}

func (randomizer) NewRandomAsset(rng *rand.Rand) channel.Asset {
	return NewRandomAsset(rng)
}

// NewRandomAsset returns a new random ethereum Asset.
func NewRandomAsset(rng *rand.Rand) *ethchannel.Asset {
	asset := ethwtest.NewRandomAddress(rng)
	chainID := NewRandomChainID(rng)
	return ethchannel.NewAssetFromEth(chainID.Int, common.Address(asset))
}

func NewRandomChainID(rng *rand.Rand) ethchannel.ChainID {
	r := rng.Uint64()
	id := new(big.Int).SetUint64(r)
	return ethchannel.MakeChainID(id)
}
