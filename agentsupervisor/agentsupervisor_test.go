package agentsupervisor

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
