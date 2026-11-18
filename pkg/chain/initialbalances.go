package chain

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/block"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/partition"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/vm"
)

// Explicit allocations are part of each owning shard's genesis state, so live
// observations and actual transfer execution use the same initial funds.
func (c *Chain) seedInitialBalances(v *vm.Executor, b *block.Block) {
	if b.Number != 1 || c.curHeader.Number != 0 {
		return
	}

	for address, balance := range c.initialBalances {
		if partition.DefaultAccountLoc(address, c.cfg.ShardNum) != c.shardID {
			continue
		}

		v.StateDB().
			SetBalance(common.Address(address), new(uint256.Int).SetBytes(balance.Bytes()), tracing.BalanceChangeReason(stateReasonBalanceInit))
	}
}

// EIP-161 can delete a fully spent zero-nonce account. An explicitly allocated
// address must then remain at zero instead of receiving the legacy lazy grant.
func (c *Chain) preserveExplicitZeroBalances(v *vm.Executor, tx transaction.Transaction) {
	for _, address := range []account.Address{tx.Sender, tx.Recipient} {
		if _, configured := c.initialBalances[address]; configured && !v.StateDB().Exist(common.Address(address)) {
			v.StateDB().
				SetBalance(common.Address(address), new(uint256.Int), tracing.BalanceChangeReason(stateReasonBalanceInit))
		}
	}
}
