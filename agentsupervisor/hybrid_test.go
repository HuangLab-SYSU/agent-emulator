package agentsupervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/HuangLab-SYSU/block-emulator-x/config"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/message"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/network"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/network/rpcserver"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/nodetopo"
)

type runtimeTestNetwork struct{ sent []*rpcserver.WrappedMsg }

func (n *runtimeTestNetwork) ListenStart() error                      { return nil }
func (n *runtimeTestNetwork) DrainMsgBuffer() []*rpcserver.WrappedMsg { return nil }
func (n *runtimeTestNetwork) SendMsg2Dest(_ context.Context, _ nodetopo.NodeInfo, msg *rpcserver.WrappedMsg) {
	n.sent = append(n.sent, msg)
}
func (n *runtimeTestNetwork) Close() {}

type runtimeTestMapper struct{}

func (runtimeTestMapper) SetTopoGetter(map[int64]map[int64]string) error { return nil }
func (runtimeTestMapper) GetNodesInShard(id int64) ([]nodetopo.NodeInfo, error) {
	return []nodetopo.NodeInfo{{ShardID: id}}, nil
}
func (runtimeTestMapper) GetLeader(id int64) (nodetopo.NodeInfo, error) {
	return nodetopo.NodeInfo{ShardID: id}, nil
}
func (runtimeTestMapper) ChangeLeader(int64, nodetopo.NodeInfo) error { return nil }
func (runtimeTestMapper) GetAllLeaders() ([]nodetopo.NodeInfo, error) {
	return []nodetopo.NodeInfo{{}}, nil
}
func (runtimeTestMapper) GetSupervisor() (nodetopo.NodeInfo, error) {
	return nodetopo.NodeInfo{ShardID: 2147483647}, nil
}

func testHybrid(t *testing.T) *HybridRuntime {
	t.Helper()
	cfg := testConfig(t)
	cfg.Chain.Enabled = true
	cfg.Base.BlockEmulatorConfig = "unused.yaml"
	cfg.Experiment.Trace = filepath.Join(t.TempDir(), "trace.jsonl")
	require.NoError(t, os.WriteFile(cfg.Experiment.Trace, []byte("{\"agent_id\":\"alice\",\"action\":\"join\",\"ts\":0}\n{\"agent_id\":\"bob\",\"action\":\"join\",\"ts\":0}\n"), 0o600))
	cfg.AgentAPI = ModelConfig{Enabled: true, AgentID: "alice", BaseURL: "http://localhost/v1", Model: "test"}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	h, err := NewHybridRuntime(network.NewConnHandler(&runtimeTestNetwork{}), runtimeTestMapper{}, config.SupervisorCfg{SystemCfg: config.SystemCfg{ShardNum: 1, ConsensusType: config.StaticRelayConsensus, AgentRuntimeConfig: path}, TxInjectionSpeed: 10})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, h.Close()) })
	return h
}

func TestHybridTraceOwnership(t *testing.T) {
	records := []Record{{AgentID: "alice", Action: ActionJoin}, {AgentID: "bob", Action: ActionJoin}, {AgentID: "alice", Action: ActionPay, Amount: 9}, {AgentID: "bob", Action: ActionPay, Target: "alice", Amount: 3}, {AgentID: "alice", Action: ActionLeave}, {AgentID: "alice", Action: ActionJoin}}
	filtered, skipped, err := FilterHybridTrace(records, "alice", 42)
	require.NoError(t, err)
	require.Len(t, filtered, 3)
	require.Equal(t, 3, skipped)
	require.Equal(t, records[3], filtered[2])
	_, _, err = FilterHybridTrace(records, "unknown", 42)
	require.Error(t, err)
	addr, err := didAddress(allocatedDID(42, "alice"))
	require.NoError(t, err)
	records = append(records, Record{RawTx: &RawTxSpec{Sender: hex.EncodeToString(addr[:])}})
	_, _, err = FilterHybridTrace(records, "alice", 42)
	require.ErrorContains(t, err, "cannot spend API")
}

func TestHybridTraceTimingAndCompilerContinuity(t *testing.T) {
	h := testHybrid(t)
	h.started = time.Now()
	h.records = append(h.records, Record{AgentID: "bob", Target: "alice", Action: ActionPay, Amount: 3, TS: 10000})
	require.NoError(t, h.injectTrace(context.Background(), h.started))
	require.Equal(t, 2, h.cursor)
	require.NoError(t, h.injectTrace(context.Background(), h.started.Add(10*time.Second)))
	require.Equal(t, 3, h.cursor)
	require.EqualValues(t, 1, h.compiler.txs[2].Nonce, "trace and API share persistent sender nonces")
	_, err := h.applyDecision(context.Background(), Decision{Action: "pay", Target: "bob", Amount: "5"}, Observation{Agents: []ObservedAgent{{AgentID: "alice", AvailableBalance: "4"}}}, time.Now())
	require.ErrorContains(t, err, "insufficient")
	hash, err := h.applyDecision(context.Background(), Decision{Action: "pay", Target: "bob", Amount: "5"}, Observation{Agents: []ObservedAgent{{AgentID: "alice", AvailableBalance: "5"}}}, time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, hash)
	require.EqualValues(t, 1, h.compiler.txs[3].Nonce)
}

func TestHybridCrossShardSettlementAndReservations(t *testing.T) {
	h := testHybrid(t)
	h.started = time.Now()
	require.NoError(t, h.injectTrace(context.Background(), h.started))
	tx := h.compiler.txs[0]
	hash, err := tx.Hash()
	require.NoError(t, err)
	key := hex.EncodeToString(hash)
	relay := tx
	relay.ROriginalHash = hash
	deliver := func(info *message.RelayBlockInfoMsg) {
		msg, err := message.WrapMsg(info)
		require.NoError(t, err)
		require.NoError(t, h.HandleMsg(context.Background(), msg))
	}
	deliver(&message.RelayBlockInfoMsg{Relay2Txs: []transaction.Transaction{relay}})
	require.Contains(t, h.pending, key, "recipient-stage report alone does not complete payment")
	deliver(&message.RelayBlockInfoMsg{Relay1Txs: []transaction.Transaction{relay}})
	require.NotContains(t, h.pending, key)
	deliver(&message.RelayBlockInfoMsg{Relay1Txs: []transaction.Transaction{relay}})
	require.NotContains(t, h.pending, key, "duplicate receipts cannot recreate a pending transaction")
	addr, err := didAddress(allocatedDID(42, "alice"))
	require.NoError(t, err)
	other, err := didAddress(allocatedDID(42, "bob"))
	require.NoError(t, err)
	h.pending = map[string]*pendingPayment{"pending": {Tx: transaction.Transaction{Sender: addr, Value: big.NewInt(3)}}}
	h.snapshots = map[int64]*message.AccountSnapshotResponse{0: {Height: 9, States: []*account.State{{Address: addr, Balance: big.NewInt(10)}, {Address: other, Balance: big.NewInt(20)}}}}
	obs, err := h.buildObservation(time.Now())
	require.NoError(t, err)
	require.Equal(t, "7", obs.Agents[0].AvailableBalance)
	h.pending["pending"].Debit = true
	obs, err = h.buildObservation(time.Now())
	require.NoError(t, err)
	require.Equal(t, "10", obs.Agents[0].AvailableBalance, "already committed debit is not reserved twice")
}

func TestHybridFailureReleasesBudget(t *testing.T) {
	h := testHybrid(t)
	h.phase = phaseThinking
	h.current = DecisionAudit{Started: time.Now()}
	h.budget.Limit(0)
	h.resultCh <- modelOutcome{Err: context.DeadlineExceeded}
	require.NoError(t, h.pollModel(time.Now()))
	require.Equal(t, phaseIdle, h.phase)
	require.NoError(t, h.budget.Request(0, 1))
	_, _, ok := h.budget.Grant()
	require.True(t, ok)
	require.Len(t, h.audits, 1)
	require.Contains(t, h.audits[0].Result, "deadline")
	raw, err := os.ReadFile(filepath.Join(h.cfg.Base.ResultDir, HybridSummaryFile))
	require.NoError(t, err)
	require.True(t, json.Valid(raw))
}
