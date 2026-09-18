package agentemu

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
	host, err := NewHost(testConfig(t))
	require.NoError(t, err)
	result, err := host.Process([]Record{
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
	require.NoError(t, host.WriteResult(t.TempDir(), result))

	registry, err := LoadRegistry(t.TempDir()+"/missing.json", 42)
	require.NoError(t, err)
	first, changed, err := registry.Join("alice")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, allocatedDID(42, "alice"), first.DID)
}

func TestPaymentRequiresJoinedAgents(t *testing.T) {
	host, err := NewHost(testConfig(t))
	require.NoError(t, err)
	_, err = host.Process([]Record{{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 7, TS: 1, Seq: 1}})
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

	host, err := NewHost(cfg)
	require.NoError(t, err)

	result, err := host.Process([]Record{
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
	require.NoError(t, host.WriteResult(outDir, result))

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

// TestHostResolvesAgentIdsInPlainTransfers verifies that plain transfer lines
// may name a currently active agent by id on either side, compiling to a
// transfer from/to that agent's DID address within the shared nonce space, and
// that the action link records the resolved agent ids and value.
func TestHostResolvesAgentIdsInPlainTransfers(t *testing.T) {
	cfg := testConfig(t)

	aliceAddr, err := didAddress(allocatedDID(cfg.Experiment.Seed, "alice"))
	require.NoError(t, err)
	bobAddr, err := didAddress(allocatedDID(cfg.Experiment.Seed, "bob"))
	require.NoError(t, err)
	normalAddr, err := utils.Hex2Addr("0x" + strings.Repeat("22", 20))
	require.NoError(t, err)

	host, err := NewHost(cfg)
	require.NoError(t, err)

	result, err := host.Process([]Record{
		{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
		// agent -> normal account, agent named by id
		{Action: ActionRawTx, RawTx: &RawTxSpec{
			Sender: "alice", Recipient: fmt.Sprintf("0x%x", normalAddr), Value: "7",
		}, TS: 3, Seq: 3},
		// normal account -> agent, agent named by id
		{Action: ActionRawTx, RawTx: &RawTxSpec{
			Sender: fmt.Sprintf("0x%x", normalAddr), Recipient: "bob", Value: "4",
		}, TS: 4, Seq: 4},
		// a later agent pay continues the sender's nonce sequence
		{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 5, TS: 5, Seq: 5},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 5)

	agentToNormal := result.Transactions[2]
	require.Equal(t, aliceAddr, agentToNormal.Sender)
	require.Equal(t, normalAddr, agentToNormal.Recipient)
	require.EqualValues(t, 7, agentToNormal.Value.Int64())
	require.Empty(t, agentToNormal.Data)
	// alice's register took nonce 0; this mixed line continues with 1.
	require.EqualValues(t, 1, agentToNormal.Nonce)

	normalToAgent := result.Transactions[3]
	require.Equal(t, normalAddr, normalToAgent.Sender)
	require.Equal(t, bobAddr, normalToAgent.Recipient)
	require.EqualValues(t, 4, normalToAgent.Value.Int64())
	// the normal account's own nonce space starts at 0.
	require.EqualValues(t, 0, normalToAgent.Nonce)

	require.EqualValues(t, 2, result.Transactions[4].Nonce)

	require.Equal(t, "alice", host.links[2].AgentID)
	require.Empty(t, host.links[2].Target)
	require.EqualValues(t, 7, host.links[2].Amount)
	require.Empty(t, host.links[3].AgentID)
	require.Equal(t, "bob", host.links[3].Target)
	require.EqualValues(t, 4, host.links[3].Amount)
}

func TestPlainTransferRejectsUnknownAndInactiveAgents(t *testing.T) {
	t.Run("unknown id that is not an address", func(t *testing.T) {
		host, err := NewHost(testConfig(t))
		require.NoError(t, err)

		_, err = host.Process([]Record{
			{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
			{Action: ActionRawTx, RawTx: &RawTxSpec{
				Sender: "alice", Recipient: "carol", Value: "1",
			}, TS: 2, Seq: 2},
		})
		require.ErrorContains(t, err, "must be a 20-byte address or an active agent id")
	})

	t.Run("agent id inactive at that point", func(t *testing.T) {
		host, err := NewHost(testConfig(t))
		require.NoError(t, err)

		_, err = host.Process([]Record{
			{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
			{AgentID: "alice", Action: ActionLeave, ParamsHash: "exit-a", TS: 2, Seq: 2},
			{Action: ActionRawTx, RawTx: &RawTxSpec{
				Sender: "alice", Recipient: "0x" + strings.Repeat("22", 20), Value: "1",
			}, TS: 3, Seq: 3},
		})
		require.ErrorContains(t, err, "join action is required")
	})
}

// TestHostActionTxMapLinksIntentToHashes verifies the request_id -> tx hash
// mapping: every action's link carries the hash of the transaction it
// compiled into, and the mapped hashes equal the plan's transaction set.
func TestHostActionTxMapLinksIntentToHashes(t *testing.T) {
	cfg := testConfig(t)
	cfg.Base.ResultDir = t.TempDir()

	host, err := NewHost(cfg)
	require.NoError(t, err)

	result, err := host.Process([]Record{
		{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
		{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 5, RequestID: "p1", TS: 3, Seq: 3},
		{AgentID: "bob", Action: ActionLeave, ParamsHash: "exit-b", TS: 4, Seq: 4},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 4)

	outDir := t.TempDir()
	require.NoError(t, host.WriteResult(outDir, result))

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
