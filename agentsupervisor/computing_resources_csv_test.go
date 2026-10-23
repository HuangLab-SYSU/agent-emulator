package agentsupervisor

import (
	"encoding/csv"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	coreblock "github.com/HuangLab-SYSU/block-emulator-x/pkg/core/block"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
)

func TestBuildRWARows(t *testing.T) {
	seller := account.Address{1}
	buyer := account.Address{2}

	sell := transaction.NewTransaction(seller, account.EmptyAccountAddr, big.NewInt(0), big.NewInt(0), 0, time.UnixMilli(1))
	sell.RWATxOpt = transaction.RWATxOpt{
		RWAAction: transaction.RWAActionSell,
		AgentID:   "seller",
		RequestID: "s1",
		ComputeID: "gpu-a100-hour",
		UnitPrice: big.NewInt(500000),
		Quantity:  100,
	}

	buy := transaction.NewTransaction(buyer, seller, big.NewInt(5000000), big.NewInt(0), 0, time.UnixMilli(2))
	buy.RWATxOpt = transaction.RWATxOpt{
		RWAAction: transaction.RWAActionBuy,
		AgentID:   "buyer",
		TargetID:  "seller",
		RequestID: "b1",
		ComputeID: "gpu-a100-hour",
		Quantity:  10,
	}

	rows := buildRWARows([][]*coreblock.Block{{newBlock(1, time.UnixMilli(10), *sell, *buy)}})
	require.Len(t, rows, 2)
	require.Equal(t, transaction.RWAActionSell, rows[0].rwaAction)
	require.Equal(t, "", rows[0].recipient)
	require.Equal(t, "500000", rows[0].unitPrice)
	require.Equal(t, transaction.RWAActionBuy, rows[1].rwaAction)
	require.Equal(t, "5000000", rows[1].value)
	require.Equal(t, "500000", rows[1].unitPrice)
	require.False(t, rows[1].insufficientBalance)
}

func TestBuildRWARowsMarksInsufficientBalance(t *testing.T) {
	seller := account.Address{1}
	buyer := account.Address{2}
	huge, ok := new(big.Int).SetString("2000000000000000000000000000000000000", 10)
	require.True(t, ok)

	buy := transaction.NewTransaction(buyer, seller, huge, big.NewInt(0), 0, time.UnixMilli(1))
	buy.RWATxOpt = transaction.RWATxOpt{
		RWAAction: transaction.RWAActionBuy,
		AgentID:   "buyer",
		TargetID:  "seller",
		RequestID: "b1",
		ComputeID: "gpu-h100-hour",
		Quantity:  1,
	}

	rows := buildRWARows([][]*coreblock.Block{{newBlock(1, time.UnixMilli(10), *buy)}})
	require.Len(t, rows, 1)
	require.True(t, rows[0].insufficientBalance)
}

// TestBuildRWARowsMarksRejectedBuy verifies that a refused buy keeps its
// zero-value record-only transfer in the orders CSV with rejected=true and
// without a derived unit price.
func TestBuildRWARowsMarksRejectedBuy(t *testing.T) {
	seller := account.Address{1}
	buyer := account.Address{2}

	buy := transaction.NewTransaction(buyer, seller, big.NewInt(0), big.NewInt(0), 0, time.UnixMilli(1))
	buy.RWATxOpt = transaction.RWATxOpt{
		RWAAction: transaction.RWAActionBuy,
		AgentID:   "buyer",
		TargetID:  "seller",
		RequestID: "b1",
		ComputeID: "gpu-a100-hour",
		Quantity:  1000,
		Rejected:  true,
	}

	rows := buildRWARows([][]*coreblock.Block{{newBlock(1, time.UnixMilli(10), *buy)}})
	require.Len(t, rows, 1)
	require.True(t, rows[0].rejected)
	require.False(t, rows[0].insufficientBalance)
	require.Equal(t, "0", rows[0].value)
	require.Equal(t, "", rows[0].unitPrice)

	records := writeAndReadOrdersCSV(t, rows)
	require.Equal(t, "true", records[0]["rejected"])
}

// writeAndReadOrdersCSV round-trips rows through writeRWAOrdersCSV so header
// and cell alignment stay honest.
func writeAndReadOrdersCSV(t *testing.T, rows []rwaTxRow) []map[string]string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "rwa_orders.csv")
	require.NoError(t, writeRWAOrdersCSV(path, rows))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	records, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(records), 2)

	out := make([]map[string]string, 0, len(records)-1)
	for _, record := range records[1:] {
		row := map[string]string{}
		for i, name := range rwaOrdersHeader {
			row[name] = record[i]
		}
		out = append(out, row)
	}

	return out
}
