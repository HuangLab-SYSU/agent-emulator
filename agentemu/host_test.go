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
)

const contract = "0x0000000000000000000000000000000000000010"

func testConfig(t *testing.T) Config {
	t.Helper()

	var cfg Config
	cfg.Base.ResultDir = t.TempDir()
	cfg.Experiment.Seed = 42
	cfg.Protocols.Pay.Plugin = "direct-pay"
	cfg.Protocols.Audit.Plugin = "merkle-audit"
	cfg.Protocols.Audit.ContractAddress = contract
	cfg.Protocols.Audit.BatchSize = 2
	cfg.Protocols.Identity.Plugin = "did-simple"
	cfg.Protocols.Identity.ContractAddress = contract
	cfg.Loop.MaxRounds = 2

	return cfg
}

func TestHostAssignsDIDsAndAuditsLifecycleAndPayment(t *testing.T) {
	host, err := NewHost(testConfig(t))
	require.NoError(t, err)
	result, err := host.Process([]Record{
		{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
		{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 3, TS: 3, Seq: 3},
		{AgentID: "bob", Action: ActionLeave, ParamsHash: "exit", TS: 4, Seq: 4},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 6)
	require.Len(t, result.Metrics, 10)
	require.Empty(t, result.Transactions[3].Data)
	require.EqualValues(t, 3, result.Transactions[3].Value.Int64())
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

// TestHostActionTxMapLinksIntentToHashes verifies the request_id -> tx hash
// mapping, including the many-to-many case: a merkle-audit anchor covers the
// buffered log entries of several actions and is attributed to all of them.
func TestHostActionTxMapLinksIntentToHashes(t *testing.T) {
	cfg := testConfig(t) // merkle-audit, batch_size: 2
	cfg.Base.ResultDir = t.TempDir()

	host, err := NewHost(cfg)
	require.NoError(t, err)

	result, err := host.Process([]Record{
		{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
		{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 5, RequestID: "p1", TS: 3, Seq: 3},
		{AgentID: "alice", Action: ActionAppendLog, ParamsHash: "log-p1", RequestID: "p1", TS: 4, Seq: 4},
		{AgentID: "bob", Action: ActionLeave, ParamsHash: "exit-b", TS: 5, Seq: 5},
	})
	require.NoError(t, err)
	require.Len(t, result.Transactions, 7)

	outDir := t.TempDir()
	require.NoError(t, host.WriteResult(outDir, result))

	raw, err := os.ReadFile(filepath.Join(outDir, ActionTxMapFileName))
	require.NoError(t, err)

	links := make([]ActionTxLink, 0, 5)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var link ActionTxLink
		require.NoError(t, json.Unmarshal([]byte(line), &link))
		links = append(links, link)
	}

	require.Len(t, links, 5) // one row per trace action

	byAction := map[string]ActionTxLink{}
	for _, link := range links {
		byAction[fmt.Sprintf("%s:%s:%d", link.Action, link.AgentID, link.TS)] = link
	}

	joinAlice := byAction["join:alice:1"]
	require.Equal(t, 2, len(joinAlice.TxHashes)) // register + shared anchor

	joinBob := byAction["join:bob:2"]
	require.Equal(t, 2, len(joinBob.TxHashes))

	// The first anchor covers both joins' log entries: shared hash.
	require.Equal(t, joinAlice.TxHashes[1], joinBob.TxHashes[1])
	require.NotEqual(t, joinAlice.TxHashes[0], joinBob.TxHashes[0]) // distinct registers

	pay := byAction["pay:alice:3"]
	require.Equal(t, "p1", pay.RequestID)
	require.EqualValues(t, 5, pay.Amount)
	require.Equal(t, 2, len(pay.TxHashes)) // transfer + shared anchor

	log := byAction["append_log:alice:4"]
	require.Equal(t, "p1", log.RequestID)

	// The second anchor covers the pay's log entry and the append_log.
	require.Equal(t, pay.TxHashes[1], log.TxHashes[0])

	leave := byAction["leave:bob:5"]
	require.Equal(t, 2, len(leave.TxHashes)) // revoke + final anchor

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
