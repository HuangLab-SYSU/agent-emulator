package tracesource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/utils"
)

const traceFixture = `{"hash":"a1","sender":"0x0000000000000000000000000000000000000001","recipient":"0x0000000000000000000000000000000000000002","value":"12","nonce":0,"data":"0x"}
{"hash":"a2","sender":"0x0000000000000000000000000000000000000001","recipient":"0x0000000000000000000000000000000000000020","value":"0","nonce":1,"data":"0x8f5bae2e"}
{"hash":"a3","sender":"0x0000000000000000000000000000000000000002","recipient":"0x0000000000000000000000000000000000000001","value":"3","nonce":0,"data":"0x"}
`

func TestTraceSourceJSONLReadsAllLinesThenExhausts(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent_transactions.jsonl")
	require.NoError(t, os.WriteFile(file, []byte(traceFixture), 0o644))

	src, err := NewTraceSourceJSONL(file)
	require.NoError(t, err)

	txs, err := src.ReadTxs(10)
	require.NoError(t, err)
	require.Len(t, txs, 3)

	sender, err := utils.Hex2Addr("0x0000000000000000000000000000000000000001")
	require.NoError(t, err)
	recipient, err := utils.Hex2Addr("0x0000000000000000000000000000000000000002")
	require.NoError(t, err)

	require.Equal(t, sender, txs[0].Sender)
	require.Equal(t, recipient, txs[0].Recipient)
	require.EqualValues(t, 12, txs[0].Value.Int64())
	require.Empty(t, txs[0].Data)
	require.Equal(t, transaction.NormalTxType, txs[0].TxType())

	require.Len(t, txs[1].Data, 4)
	require.EqualValues(t, 1, txs[1].Nonce)
	require.Equal(t, transaction.CallContractTxType, txs[1].TxType())

	// Exhausted source keeps returning (nil, nil).
	txs, err = src.ReadTxs(10)
	require.NoError(t, err)
	require.Empty(t, txs)
}

func TestTraceSourceJSONLRejectsBrokenLine(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent_transactions.jsonl")
	require.NoError(t, os.WriteFile(file, []byte("{not json}\n"), 0o644))

	src, err := NewTraceSourceJSONL(file)
	require.NoError(t, err)

	_, err = src.ReadTxs(10)
	require.ErrorContains(t, err, "decode trace line 1")
}

func TestTraceSourceJSONLMissingFile(t *testing.T) {
	_, err := NewTraceSourceJSONL(filepath.Join(t.TempDir(), "missing.jsonl"))
	require.ErrorContains(t, err, "failed to open trace file")
}

func TestTraceSourceJSONLReadsRWATransactions(t *testing.T) {
	fixture := `{"hash":"s1","sender":"0x0000000000000000000000000000000000000001","recipient":"","value":"","nonce":0,"data":"0x","rwa_action":"sell","agent_id":"seller","request_id":"sell-1","compute_id":"gpu-a100-hour","unit_price":"500000","quantity":100}
{"hash":"b1","sender":"0x0000000000000000000000000000000000000002","recipient":"0x0000000000000000000000000000000000000001","value":"5000000","nonce":0,"data":"0x","rwa_action":"buy","agent_id":"buyer","target_id":"seller","request_id":"buy-1","compute_id":"gpu-a100-hour","quantity":10}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "agent_transactions.jsonl")
	require.NoError(t, os.WriteFile(file, []byte(fixture), 0o644))

	src, err := NewTraceSourceJSONL(file)
	require.NoError(t, err)

	txs, err := src.ReadTxs(10)
	require.NoError(t, err)
	require.Len(t, txs, 2)
	require.True(t, txs[0].IsRWASell())
	require.Equal(t, transaction.NormalTxType, txs[0].TxType())
	require.Empty(t, txs[0].Data)
	require.Equal(t, "500000", txs[0].UnitPrice.String())
	require.True(t, txs[1].IsRWABuy())
	require.Equal(t, "5000000", txs[1].Value.String())
	require.EqualValues(t, 10, txs[1].Quantity)
}

func TestTraceSourceJSONLRejectsRWAData(t *testing.T) {
	fixture := `{"hash":"s1","sender":"0x0000000000000000000000000000000000000001","recipient":"","value":"","nonce":0,"data":"0x1234","rwa_action":"sell","agent_id":"seller","request_id":"sell-1","compute_id":"gpu-a100-hour","unit_price":"500000","quantity":100}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "agent_transactions.jsonl")
	require.NoError(t, os.WriteFile(file, []byte(fixture), 0o644))

	src, err := NewTraceSourceJSONL(file)
	require.NoError(t, err)
	_, err = src.ReadTxs(10)
	require.ErrorContains(t, err, "rwa transaction data must be empty")
}
