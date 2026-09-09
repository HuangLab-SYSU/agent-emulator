package agentemu

import (
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
	require.NoError(t, host.WriteResult(result))

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
