package chain

import (
	"context"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/HuangLab-SYSU/block-emulator-x/config"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/block"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
)

func TestInitialBalanceFundingAndZeroPersistence(t *testing.T) {
	for _, initial := range []string{"0", "2"} {
		t.Run(initial, func(t *testing.T) {
			cfg := getTestConfig()
			cfg.ShardNum = 1
			cfg.BoltCfg.FilePathDir = filepath.Join(t.TempDir(), "bolt")
			cfg.InitialBalances = map[string]string{fmt.Sprintf("0x%x", testSender): initial}
			bc, err := NewChain(cfg, config.LocalParams{ShardID: 0})
			require.NoError(t, err)
			defer func() { require.NoError(t, bc.Close()) }()
			ctx := context.Background()
			balance := func(address account.Address) string {
				states, err := bc.GetAccountStates(ctx, []account.Address{address})
				require.NoError(t, err)
				return states[0].Balance.String()
			}
			require.Equal(t, initial, balance(testSender))
			transact := func(from, to account.Address, value int64) {
				tx := transaction.NewTransaction(from, to, big.NewInt(value), big.NewInt(0), 0, time.Now())
				b, err := bc.GenerateBlock(ctx, testMiner, block.TxBlockType, block.Body{TxList: []transaction.Transaction{*tx}}, block.MigrationOpt{})
				require.NoError(t, err)
				validateAndAddBlock(t, bc, b)
			}
			// Spend the whole explicit allocation, then receive money again. Neither
			// snapshots nor transfer execution may resurrect the huge legacy balance.
			if initial == "2" {
				transact(testSender, testRecipient, 2)
			}
			require.Equal(t, "0", balance(testSender))
			transact(testRecipient, testSender, 3)
			require.Equal(t, "3", balance(testSender))
			transact(testSender, testRecipient, 3)
			require.Equal(t, "0", balance(testSender))
			tx := transaction.NewTransaction(testSender, testRecipient, big.NewInt(1), big.NewInt(0), 0, time.Now())
			_, err = bc.GenerateBlock(ctx, testMiner, block.TxBlockType, block.Body{TxList: []transaction.Transaction{*tx}}, block.MigrationOpt{})
			require.ErrorContains(t, err, "not enough")
			require.Equal(t, "0", balance(testSender))
		})
	}
}
