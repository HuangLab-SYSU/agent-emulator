package agentsupervisor

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/utils"
)

const contract = "0x0000000000000000000000000000000000000010"

func testConfig(t *testing.T) Config {
	t.Helper()

	var cfg Config
	cfg.Base.ResultDir = t.TempDir()
	cfg.Experiment.Seed = 42
	cfg.Protocols.Pay.Plugin = "direct-pay"
	cfg.Protocols.Identity.Plugin = "did-simple"
	cfg.Protocols.Identity.ContractAddress = contract
	cfg.Loop.MaxRounds = 2

	return cfg
}

func TestHostAssignsDIDsAndCompilesLifecycleAndPayment(t *testing.T) {
	sup, err := NewAgentSupervisor(testConfig(t))
	require.NoError(t, err)
	result, err := sup.Process([]Record{
		{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
		{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 3, TS: 3, Seq: 3},
		{AgentID: "bob", Action: ActionLeave, ParamsHash: "exit", TS: 4, Seq: 4},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 4)
	require.Len(t, result.Metrics, 4)
	require.Empty(t, result.Transactions[2].Data)
	require.EqualValues(t, 3, result.Transactions[2].Value.Int64())
	require.NoError(t, sup.WriteResult(t.TempDir(), result))

	registry, err := LoadRegistry(t.TempDir()+"/missing.json", 42)
	require.NoError(t, err)
	first, changed, err := registry.Join("alice")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, allocatedDID(42, "alice"), first.DID)
}

func TestPaymentRequiresJoinedAgents(t *testing.T) {
	sup, err := NewAgentSupervisor(testConfig(t))
	require.NoError(t, err)
	_, err = sup.Process([]Record{{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 7, TS: 1, Seq: 1}})
	require.ErrorContains(t, err, "join action is required")
}

// TestHostCompilesPlainTransfersWithSharedNonces verifies that a plain
// transfer line compiles like any other transaction: empty data and a nonce
// from the shared per-sender counter, even when its sender is an agent's DID.
func TestHostCompilesPlainTransfersWithSharedNonces(t *testing.T) {
	cfg := testConfig(t)

	aliceAddr, err := didAddress(allocatedDID(cfg.Experiment.Seed, "alice"))
	require.NoError(t, err)

	spec := &RawTxSpec{
		Sender:    fmt.Sprintf("0x%x", aliceAddr),
		Recipient: "0x" + strings.Repeat("22", 20),
		Value:     "9",
	}

	sup, err := NewAgentSupervisor(cfg)
	require.NoError(t, err)

	result, err := sup.Process([]Record{
		{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
		{Action: ActionRawTx, RawTx: spec, TS: 2, Seq: 3},
		{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 5, TS: 3, Seq: 4},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 4)

	raw := result.Transactions[2]
	require.Equal(t, aliceAddr, raw.Sender)

	expectedTo, err := utils.Hex2Addr(spec.Recipient)
	require.NoError(t, err)
	require.Equal(t, expectedTo, raw.Recipient)
	require.EqualValues(t, 9, raw.Value.Int64())
	require.Empty(t, raw.Data)

	// Shared per-sender nonce space: alice's register is nonce 0, the plain
	// transfer takes 1 and the later pay takes 2.
	require.EqualValues(t, 1, raw.Nonce)
	require.EqualValues(t, 2, result.Transactions[3].Nonce)

	var rawEvent *MetricEvent
	for i := range result.Metrics {
		if result.Metrics[i].Kind == "raw_tx" {
			rawEvent = &result.Metrics[i]
		}
	}
	require.NotNil(t, rawEvent)
	require.EqualValues(t, 9, rawEvent.Value)

	// The plain transfer appears in the action map with exactly one hash, and
	// the mapped hashes still equal the plan's transaction set.
	outDir := t.TempDir()
	require.NoError(t, sup.WriteResult(outDir, result))

	rawMap, err := os.ReadFile(filepath.Join(outDir, ActionTxMapFileName))
	require.NoError(t, err)

	links := make([]ActionTxLink, 0, 4)
	for _, line := range strings.Split(strings.TrimSpace(string(rawMap)), "\n") {
		var link ActionTxLink
		require.NoError(t, json.Unmarshal([]byte(line), &link))
		links = append(links, link)
	}

	require.Len(t, links, 4)
	require.Equal(t, ActionRawTx, links[2].Action)
	require.Len(t, links[2].TxHashes, 1)

	planHashes := map[string]struct{}{}
	for _, tx := range result.Transactions {
		hash, err := tx.Hash()
		require.NoError(t, err)
		planHashes[hex.EncodeToString(hash)] = struct{}{}
	}

	mapped := map[string]struct{}{}
	for _, link := range links {
		for _, hash := range link.TxHashes {
			mapped[hash] = struct{}{}
		}
	}

	require.Equal(t, planHashes, mapped)
}

// TestHostActionTxMapLinksIntentToHashes verifies the request_id -> tx hash
// mapping: every action's link carries the hash of the transaction it
// compiled into, and the mapped hashes equal the plan's transaction set.
func TestHostActionTxMapLinksIntentToHashes(t *testing.T) {
	cfg := testConfig(t)
	cfg.Base.ResultDir = t.TempDir()

	sup, err := NewAgentSupervisor(cfg)
	require.NoError(t, err)

	result, err := sup.Process([]Record{
		{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
		{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 5, RequestID: "p1", TS: 3, Seq: 3},
		{AgentID: "bob", Action: ActionLeave, ParamsHash: "exit-b", TS: 4, Seq: 4},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 4)

	outDir := t.TempDir()
	require.NoError(t, sup.WriteResult(outDir, result))

	raw, err := os.ReadFile(filepath.Join(outDir, ActionTxMapFileName))
	require.NoError(t, err)

	links := make([]ActionTxLink, 0, 4)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var link ActionTxLink
		require.NoError(t, json.Unmarshal([]byte(line), &link))
		links = append(links, link)
	}

	require.Len(t, links, 4) // one row per trace action

	byAction := map[string]ActionTxLink{}
	for _, link := range links {
		byAction[fmt.Sprintf("%s:%s:%d", link.Action, link.AgentID, link.TS)] = link
	}

	joinAlice := byAction["join:alice:1"]
	require.Len(t, joinAlice.TxHashes, 1) // register

	joinBob := byAction["join:bob:2"]
	require.Len(t, joinBob.TxHashes, 1)
	require.NotEqual(t, joinAlice.TxHashes[0], joinBob.TxHashes[0]) // distinct registers

	pay := byAction["pay:alice:3"]
	require.Equal(t, "p1", pay.RequestID)
	require.EqualValues(t, 5, pay.Amount)
	require.Len(t, pay.TxHashes, 1) // transfer

	leave := byAction["leave:bob:4"]
	require.Len(t, leave.TxHashes, 1) // revoke

	// Every mapped hash exists in the plan file (same transaction set).
	planHashes := map[string]struct{}{}
	for _, tx := range result.Transactions {
		hash, err := tx.Hash()
		require.NoError(t, err)
		planHashes[hex.EncodeToString(hash)] = struct{}{}
	}

	mapped := map[string]struct{}{}
	for _, link := range links {
		for _, hash := range link.TxHashes {
			mapped[hash] = struct{}{}
		}
	}

	require.Equal(t, planHashes, mapped)
}

func TestHostCompilesRWABuySell(t *testing.T) {
	sup, err := NewAgentSupervisor(testConfig(t))
	require.NoError(t, err)

	result, err := sup.Process([]Record{
		{AgentID: "seller", Action: ActionJoin, ParamsHash: "seller-doc", TS: 1, Seq: 1},
		{AgentID: "buyer", Action: ActionJoin, ParamsHash: "buyer-doc", TS: 2, Seq: 2},
		{AgentID: "seller", Action: ActionSell, ComputeID: "gpu-a100-hour", UnitPrice: "500000", Quantity: 100, RequestID: "s1", TS: 3, Seq: 3},
		{AgentID: "buyer", Target: "seller", Action: ActionBuy, ComputeID: "gpu-a100-hour", Quantity: 10, RequestID: "b1", TS: 4, Seq: 4},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 4)

	sell := result.Transactions[2]
	require.True(t, sell.IsRWASell())
	require.Equal(t, transaction.NormalTxType, sell.TxType())
	require.EqualValues(t, account.EmptyAccountAddr, sell.Recipient)
	require.Equal(t, "0", sell.Value.String())
	require.Empty(t, sell.Data)
	require.Equal(t, "seller", sell.AgentID)
	require.Equal(t, "gpu-a100-hour", sell.ComputeID)
	require.EqualValues(t, 100, sell.Quantity)
	require.Equal(t, big.NewInt(500000), sell.UnitPrice)

	buy := result.Transactions[3]
	require.True(t, buy.IsRWABuy())
	require.Equal(t, transaction.NormalTxType, buy.TxType())
	require.Empty(t, buy.Data)
	require.Equal(t, "buyer", buy.AgentID)
	require.Equal(t, "seller", buy.TargetID)
	require.Equal(t, "gpu-a100-hour", buy.ComputeID)
	require.EqualValues(t, 10, buy.Quantity)
	require.Equal(t, "5000000", buy.Value.String())

	outDir := t.TempDir()
	require.NoError(t, sup.WriteResult(outDir, result))
	planRaw, err := os.ReadFile(filepath.Join(outDir, PlanFileName))
	require.NoError(t, err)
	require.Contains(t, string(planRaw), `"rwa_action":"sell"`)
	require.Contains(t, string(planRaw), `"rwa_action":"buy"`)
	require.Contains(t, string(planRaw), `"recipient":""`)
	require.Contains(t, string(planRaw), `"value":""`)
}

func TestHostRWABuyRequiresSellQuote(t *testing.T) {
	sup, err := NewAgentSupervisor(testConfig(t))
	require.NoError(t, err)

	_, err = sup.Process([]Record{
		{AgentID: "seller", Action: ActionJoin, ParamsHash: "seller-doc", TS: 1, Seq: 1},
		{AgentID: "buyer", Action: ActionJoin, ParamsHash: "buyer-doc", TS: 2, Seq: 2},
		{AgentID: "buyer", Target: "seller", Action: ActionBuy, ComputeID: "gpu-a100-hour", Quantity: 10, RequestID: "b1", TS: 3, Seq: 3},
	})
	require.ErrorContains(t, err, "missing sell quote")
}

// TestRWABuyInventoryAccounting covers the listing-stock lifecycle: a buy
// exceeding the remaining stock is refused as a zero-value record-only
// transfer, a repeated sell accumulates into the remaining stock, and the
// retry after re-listing settles at unit_price × quantity. The refused buys
// never move the stock.
func TestRWABuyInventoryAccounting(t *testing.T) {
	cfg := testConfig(t)
	sup, err := NewAgentSupervisor(cfg)
	require.NoError(t, err)

	sellerAddr, err := didAddress(allocatedDID(cfg.Experiment.Seed, "seller"))
	require.NoError(t, err)

	result, err := sup.Process([]Record{
		{AgentID: "seller", Action: ActionJoin, ParamsHash: "seller-doc", TS: 1, Seq: 1},
		{AgentID: "buyer", Action: ActionJoin, ParamsHash: "buyer-doc", TS: 2, Seq: 2},
		{AgentID: "seller", Action: ActionSell, ComputeID: "gpu-a100-hour", UnitPrice: "1000", Quantity: 500, RequestID: "s1", TS: 3, Seq: 3},
		{AgentID: "buyer", Target: "seller", Action: ActionBuy, ComputeID: "gpu-a100-hour", Quantity: 1000, RequestID: "b1", TS: 4, Seq: 4},
		{AgentID: "seller", Action: ActionSell, ComputeID: "gpu-a100-hour", UnitPrice: "1000", Quantity: 500, RequestID: "s2", TS: 5, Seq: 5},
		{AgentID: "buyer", Target: "seller", Action: ActionBuy, ComputeID: "gpu-a100-hour", Quantity: 1000, RequestID: "b2", TS: 6, Seq: 6},
		{AgentID: "buyer", Target: "seller", Action: ActionBuy, ComputeID: "gpu-a100-hour", Quantity: 1, RequestID: "b3", TS: 7, Seq: 7},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 7) // 2 registers + 2 sells + 3 buys

	// First buy exceeds the 500 listing: refused, zero value, recipient is
	// still the seller so the intent is auditable on-chain.
	rejected1 := result.Transactions[3]
	require.True(t, rejected1.IsRWABuyRejected())
	require.True(t, rejected1.IsRWABuy())
	require.Equal(t, sellerAddr, rejected1.Recipient)
	require.Zero(t, rejected1.Value.Int64())
	require.Empty(t, rejected1.Data)
	require.EqualValues(t, 1000, rejected1.Quantity)

	// Second buy settles after the seller re-listed 500 more (500+500=1000).
	settled := result.Transactions[5]
	require.True(t, settled.IsRWABuy())
	require.False(t, settled.IsRWABuyRejected())
	require.Equal(t, sellerAddr, settled.Recipient)
	require.Equal(t, "1000000", settled.Value.String()) // 1000 × 1000

	// Third buy hits an empty stock (1000-1000=0): refused again.
	rejected2 := result.Transactions[6]
	require.True(t, rejected2.IsRWABuyRejected())
	require.Zero(t, rejected2.Value.Int64())

	// One metric per action; refused buys carry their own kind.
	kinds := make([]string, 0, len(result.Metrics))
	for _, event := range result.Metrics {
		kinds = append(kinds, event.Kind)
	}
	require.Equal(t, []string{
		"did_register", "did_register",
		"rwa_sell_onchain", "rwa_buy_rejected",
		"rwa_sell_onchain", "rwa_buy_onchain",
		"rwa_buy_rejected",
	}, kinds)

	// The action map and the plan carry the refused flag end-to-end.
	outDir := t.TempDir()
	require.NoError(t, sup.WriteResult(outDir, result))

	links := readActionTxLinks(t, filepath.Join(outDir, ActionTxMapFileName))
	require.Len(t, links, 7)
	require.False(t, links[5].Rejected)
	require.True(t, links[3].Rejected)
	require.True(t, links[6].Rejected)
	require.Len(t, links[3].TxHashes, 1) // refused buys still map to their record tx

	planRaw, err := os.ReadFile(filepath.Join(outDir, PlanFileName))
	require.NoError(t, err)
	require.Contains(t, string(planRaw), `"rwa_rejected":true`)
	require.Len(t, findAllPlanLines(t, string(planRaw)), 7)
}

func readActionTxLinks(t *testing.T, path string) []ActionTxLink {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	links := make([]ActionTxLink, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var link ActionTxLink
		require.NoError(t, json.Unmarshal([]byte(line), &link))
		links = append(links, link)
	}

	return links
}

func findAllPlanLines(t *testing.T, raw string) []map[string]any {
	t.Helper()

	lines := make([]map[string]any, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &obj))
		lines = append(lines, obj)
	}

	return lines
}
