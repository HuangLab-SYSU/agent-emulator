package agentsupervisor

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/block"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
)

func agentCSVAddr(b byte) account.Address {
	var addr account.Address
	addr[0] = b

	return addr
}

// newBlock builds a minimal synthetic block; buildAgentRows only reads
// Number, CreateTime and TxList.
func newBlock(height uint64, created time.Time, txs ...transaction.Transaction) *block.Block {
	return block.NewBlock(
		block.Header{Number: height, CreateTime: created},
		block.Body{TxList: txs},
		block.MigrationOpt{},
	)
}

func initBalance(t *testing.T) *big.Int {
	t.Helper()

	init, ok := new(big.Int).SetString(account.NormalInitBalanceStr, 10)
	require.True(t, ok)

	return init
}

func TestBuildAgentRowsInnerShardTransfer(t *testing.T) {
	alice, bob := agentCSVAddr(1), agentCSVAddr(2)
	agents := map[account.Address]string{alice: "alice", bob: "bob"}

	ts := time.UnixMilli(1000)
	tx := *transaction.NewTransaction(alice, bob, big.NewInt(7), big.NewInt(0), 0, ts)

	rows := buildAgentRows([][]*block.Block{{newBlock(3, ts, tx)}}, agents)

	require.Len(t, rows["alice"], 1)
	require.Len(t, rows["bob"], 1)

	debit := rows["alice"][0]
	credit := rows["bob"][0]
	require.EqualValues(t, 3, debit.blockHeight)
	require.EqualValues(t, 3, credit.blockHeight)
	require.Equal(t, debit.txHash, credit.txHash)
	require.Equal(t, "7", debit.value)
	require.Equal(t, "7", credit.value)

	expected, _ := new(big.Int).SetString(account.NormalInitBalanceStr, 10)
	require.Equal(t, new(big.Int).Sub(expected, big.NewInt(7)).String(), debit.balance)
	require.Equal(t, new(big.Int).Add(expected, big.NewInt(7)).String(), credit.balance)
	require.EqualValues(t, 1000, debit.blockTimeMs)
}

func TestBuildAgentRowsCrossShardRelayRecordsEachSideOnce(t *testing.T) {
	alice, bob := agentCSVAddr(1), agentCSVAddr(2)
	agents := map[account.Address]string{alice: "alice", bob: "bob"}

	ts := time.UnixMilli(1000)

	relay1 := *transaction.NewTransaction(alice, bob, big.NewInt(5), big.NewInt(0), 0, ts)
	relay1.RelayStage = transaction.Relay1Tx
	relay1.ROriginalHash = []byte("orig")

	relay2 := relay1
	relay2.RelayStage = transaction.Relay2Tx

	// relay1 commits in the sender's shard, relay2 in the recipient's shard,
	// at different heights.
	perShard := [][]*block.Block{
		{newBlock(2, ts, relay1)},
		{newBlock(4, ts.Add(time.Second), relay2)},
	}

	rows := buildAgentRows(perShard, agents)

	require.Len(t, rows["alice"], 1) // debit from the relay1 block only
	require.Len(t, rows["bob"], 1)   // credit from the relay2 block only

	require.EqualValues(t, 2, rows["alice"][0].blockHeight)
	require.EqualValues(t, 4, rows["bob"][0].blockHeight)

	// Both rows carry the LOGICAL transaction's hash (the original, which
	// relay_stats_detail_tx_info.csv and agent_action_txs.jsonl also record);
	// heights and timestamps stay the real per-leg ones: each side records
	// the commit time of the block that executed it.
	require.Equal(t, hex.EncodeToString([]byte("orig")), rows["alice"][0].txHash)
	require.Equal(t, rows["alice"][0].txHash, rows["bob"][0].txHash)
	require.EqualValues(t, 1000, rows["alice"][0].blockTimeMs)
	require.EqualValues(t, 2000, rows["bob"][0].blockTimeMs)

	expected := initBalance(t)
	require.Equal(t, new(big.Int).Sub(expected, big.NewInt(5)).String(), rows["alice"][0].balance)
	require.Equal(t, new(big.Int).Add(expected, big.NewInt(5)).String(), rows["bob"][0].balance)
}

func TestBuildAgentRowsContractTxKeepsBalance(t *testing.T) {
	alice, contract := agentCSVAddr(1), agentCSVAddr(9)
	agents := map[account.Address]string{alice: "alice"}

	ts := time.UnixMilli(1000)
	register := *transaction.NewTransaction(alice, contract, big.NewInt(0), big.NewInt(0), 0, ts)
	register.Data = []byte{1, 2, 3}

	rows := buildAgentRows([][]*block.Block{{newBlock(1, ts, register)}}, agents)

	require.Len(t, rows["alice"], 1)
	require.Equal(t, "0", rows["alice"][0].value)
	require.Equal(t, initBalance(t).String(), rows["alice"][0].balance)
}

func TestBuildAgentRowsOrdersByBlockCommitTime(t *testing.T) {
	alice := agentCSVAddr(1)
	agents := map[account.Address]string{alice: "alice"}

	// t1 was created earlier but its block committed later: rows must follow
	// the blocks' commit order, not the transactions' creation order.
	t1 := *transaction.NewTransaction(alice, agentCSVAddr(2), big.NewInt(1), big.NewInt(0), 0, time.UnixMilli(1000))
	t2 := *transaction.NewTransaction(alice, agentCSVAddr(3), big.NewInt(1), big.NewInt(0), 1, time.UnixMilli(4000))

	perShard := [][]*block.Block{
		{newBlock(9, time.UnixMilli(2000), t2)},
		{newBlock(1, time.UnixMilli(3000), t1)},
	}

	rows := buildAgentRows(perShard, agents)

	require.Len(t, rows["alice"], 2)
	require.EqualValues(t, 9, rows["alice"][0].blockHeight)
	require.EqualValues(t, 2000, rows["alice"][0].blockTimeMs)
	require.EqualValues(t, 1, rows["alice"][1].blockHeight)
	require.EqualValues(t, 3000, rows["alice"][1].blockTimeMs)

	expected := initBalance(t)
	require.Equal(t, new(big.Int).Sub(expected, big.NewInt(1)).String(), rows["alice"][0].balance)
	require.Equal(t, new(big.Int).Sub(expected, big.NewInt(2)).String(), rows["alice"][1].balance)
}

func TestBuildAgentRowsSameBlockTimeTieBreaksByShardHeight(t *testing.T) {
	alice := agentCSVAddr(1)
	agents := map[account.Address]string{alice: "alice"}

	// Blocks can be stamped within the same instant; the order then falls
	// back to (shard, height, in-block index) to stay deterministic.
	same := time.UnixMilli(1000)
	t1 := *transaction.NewTransaction(alice, agentCSVAddr(2), big.NewInt(1), big.NewInt(0), 0, same)
	t2 := *transaction.NewTransaction(alice, agentCSVAddr(3), big.NewInt(1), big.NewInt(0), 1, same)

	perShard := [][]*block.Block{
		{newBlock(7, same, t2)},
		{newBlock(3, same, t1)},
	}

	rows := buildAgentRows(perShard, agents)

	require.Len(t, rows["alice"], 2)
	// Shard 0 wins the tie even though its height is the higher one.
	require.EqualValues(t, 7, rows["alice"][0].blockHeight)
	require.EqualValues(t, 3, rows["alice"][1].blockHeight)
}

func TestAgentFileNameSanitizes(t *testing.T) {
	require.Equal(t, "agent-001.csv", agentFileName("agent-001"))
	require.Equal(t, "a_b_c.csv", agentFileName("a/b\\c"))
}
