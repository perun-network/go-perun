package multiledger

import (
	"math/big"

	"perun.network/go-perun/backend/ethereum/channel"
)

type ChainID *big.Int

type Funder struct {
	ledgers map[ChainID]*channel.ContractBackend
}

func (f *Funder) RegisterLedger(id ChainID, cb channel.ContractBackend) {

}
