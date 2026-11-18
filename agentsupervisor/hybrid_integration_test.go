package agentsupervisor

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/partition"
)

// Opt in because this builds binaries and binds the repository's cluster ports.
func TestHybridPersistentChainIntegration(t *testing.T) {
	if os.Getenv("AGENTEMU_INTEGRATION") != "1" {
		t.Skip("set AGENTEMU_INTEGRATION=1 to launch a 2-shard PBFT cluster")
	}
	wd, err := os.Getwd()
	require.NoError(t, err)
	root := filepath.Dir(wd)
	cfg := testConfig(t)
	cfg.Base.ModuleRoot = root
	cfg.Experiment.Seed = 42
	cfg.Chain.Enabled = true
	cfg.Chain.RunTimeoutSeconds = 150
	aliceAddr, err := didAddress(allocatedDID(42, "alice"))
	require.NoError(t, err)
	bob := "bob"
	for i := 0; ; i++ {
		addr, err := didAddress(allocatedDID(42, bob))
		require.NoError(t, err)
		if partition.DefaultAccountLoc(addr, 2) != partition.DefaultAccountLoc(aliceAddr, 2) {
			break
		}
		bob = fmt.Sprintf("bob-%d", i)
	}
	cfg.Experiment.Trace = filepath.Join(t.TempDir(), "trace.jsonl")
	trace := fmt.Sprintf("{\"agent_id\":\"alice\",\"action\":\"join\",\"ts\":0}\n{\"agent_id\":%q,\"action\":\"join\",\"ts\":0}\n{\"agent_id\":\"alice\",\"action\":\"pay\",\"target\":%q,\"amount\":999,\"ts\":1000}\n{\"agent_id\":%q,\"action\":\"pay\",\"target\":\"alice\",\"amount\":3,\"ts\":5000}\n{\"agent_id\":%q,\"action\":\"pay\",\"target\":\"alice\",\"amount\":4,\"ts\":12000}\n", bob, bob, bob, bob)
	require.NoError(t, os.WriteFile(cfg.Experiment.Trace, []byte(trace), 0o600))
	data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	require.NoError(t, err)
	var template map[string]any
	require.NoError(t, yaml.Unmarshal(data, &template))
	setYAMLPath(template, []string{"system", "shard_num"}, 2)
	setYAMLPath(template, []string{"system", "node_num"}, 4)
	setYAMLPath(template, []string{"system", "consensus_type"}, "static_relay")
	setYAMLPath(template, []string{"system", "log", "log_level"}, "warn")
	setYAMLPath(template, []string{"consensus_node", "block_interval"}, 500)
	setYAMLPath(template, []string{"network", "communication_mode"}, "direct")
	data, err = yaml.Marshal(template)
	require.NoError(t, err)
	cfg.Base.BlockEmulatorConfig = filepath.Join(t.TempDir(), "chain.yaml")
	require.NoError(t, os.WriteFile(cfg.Base.BlockEmulatorConfig, data, 0o600))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return
		}
		call := calls.Add(1)
		delay := 8 * time.Second
		if call > 1 {
			delay = 25 * time.Second
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		content := fmt.Sprintf(`{"action":"pay","target":%q,"amount":"7"}`, bob)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	budget := uint64(2)
	cfg.AgentAPI = ModelConfig{Enabled: true, AgentID: "alice", BaseURL: server.URL + "/v1", Model: "local-test", Goal: "pay the other agent", MaxBlocksDuringDecision: &budget, DecisionTimeoutSeconds: 10, DecisionIntervalSeconds: 1, RunDurationSeconds: 48, DrainTimeoutSeconds: 40}
	require.NoError(t, cfg.AgentAPI.normalize(true))
	chain := &ChainRunner{ModuleRoot: root, BaseConfig: cfg.Base.BlockEmulatorConfig, WorkRoot: cfg.Base.ResultDir, RunTimeout: 150 * time.Second, NodeExitGrace: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, chain.Build(ctx))
	rounds, err := RunHybrid(ctx, cfg, chain)
	require.NoError(t, err)
	require.Len(t, rounds, 1)
	data, err = os.ReadFile(filepath.Join(cfg.Base.ResultDir, HybridSummaryFile))
	require.NoError(t, err)
	var summary struct {
		Phase                string          `json:"phase"`
		PendingTransactions  int             `json:"pending_transactions"`
		Latest               Observation     `json:"latest_snapshot"`
		Decisions            []DecisionAudit `json:"decisions"`
		TraceActionsReplaced int             `json:"trace_actions_replaced"`
		BlocksCommitted      uint64          `json:"blocks_committed"`
		BlockCommits         []blockCommit   `json:"block_commits"`
	}
	require.NoError(t, json.Unmarshal(data, &summary))
	require.Equal(t, phaseFinished, summary.Phase)
	require.Zero(t, summary.PendingTransactions)
	require.Equal(t, 1, summary.TraceActionsReplaced)
	require.GreaterOrEqual(t, len(summary.Decisions), 2)
	require.Len(t, summary.BlockCommits, int(summary.BlocksCommitted))
	seen := make(map[[2]uint64]bool)
	for _, commit := range summary.BlockCommits {
		key := [2]uint64{uint64(commit.Shard), commit.Height}
		require.False(t, seen[key], "each shard block should be recorded once")
		seen[key] = true
		require.Greater(t, commit.Height, uint64(1))
		require.False(t, commit.Time.IsZero())
	}
	accepted := int64(0)
	failed := false
	saturated := false
	for _, audit := range summary.Decisions {
		require.NotNil(t, audit.SnapshotStarted)
		require.False(t, audit.SnapshotStarted.After(audit.Started))
		require.False(t, audit.Finished.Before(audit.Started))
		require.LessOrEqual(t, audit.BlocksGranted, budget)
		require.LessOrEqual(t, audit.BlocksCommitted, budget)
		if audit.BlocksGranted == budget {
			saturated = true
		}
		if audit.Result == "accepted" {
			accepted++
		} else {
			failed = true
		}
	}
	require.Positive(t, accepted)
	require.True(t, failed, "a slow subsequent model call should time out and release permits")
	require.True(t, saturated, "slow model must exercise full global block budget")
	initial, ok := new(big.Int).SetString(account.NormalInitBalanceStr, 10)
	require.True(t, ok)
	expected := new(big.Int).Add(initial, big.NewInt(7-7*accepted))
	for _, agent := range summary.Latest.Agents {
		if agent.AgentID == "alice" {
			require.Equal(t, expected.String(), agent.Balance)
		}
	}
	require.Len(t, summary.Latest.Heights, 2)
	for shard, height := range summary.Latest.Heights {
		require.Greater(t, height, uint64(1))
		require.Equal(t, height, summary.Latest.LatestBlocks[shard].Height)
		require.NotEmpty(t, summary.Latest.LatestBlocks[shard].Hash)
	}
	thinkingLog, err := os.ReadFile(filepath.Join(cfg.Base.ResultDir, AgentThinkingLogFile))
	require.NoError(t, err)
	for _, event := range []string{"thinking_started", "thinking_finished", "decision_result"} {
		require.Equal(t, len(summary.Decisions), strings.Count(string(thinkingLog), "event="+event))
	}
	require.Contains(t, string(thinkingLog), "action=pay target="+bob+" amount=7")
	require.Contains(t, string(thinkingLog), "result=accepted")
	consoleLog, err := os.ReadFile(filepath.Join(rounds[0].OutputDir, "chain", "logs", "supervisor.log"))
	require.NoError(t, err)
	require.Contains(t, string(consoleLog), "event=thinking_started", "thinking logs stay visible even with chain log level WARN")
	require.FileExists(t, filepath.Join(rounds[0].OutputDir, AgentsDirName, "alice.csv"))
}

func TestHybridWaitForFundsIntegration(t *testing.T) {
	if os.Getenv("AGENTEMU_INTEGRATION") != "1" {
		t.Skip("set AGENTEMU_INTEGRATION=1 to launch the funding scenario")
	}
	wd, err := os.Getwd()
	require.NoError(t, err)
	root := filepath.Dir(wd)
	cfg := testConfig(t)
	cfg.Base.ModuleRoot = root
	cfg.Experiment.Seed = 42
	cfg.Chain.Enabled = true
	alice, err := didAddress(allocatedDID(42, "alice"))
	require.NoError(t, err)
	bob := "bob"
	for i := 0; ; i++ {
		address, err := didAddress(allocatedDID(42, bob))
		require.NoError(t, err)
		if partition.DefaultAccountLoc(address, 2) != partition.DefaultAccountLoc(alice, 2) {
			break
		}
		bob = fmt.Sprintf("bob-%d", i)
	}
	cfg.Experiment.Trace = filepath.Join(t.TempDir(), "funding.jsonl")
	trace := fmt.Sprintf("{\"agent_id\":\"alice\",\"action\":\"join\",\"ts\":0}\n{\"agent_id\":%q,\"action\":\"join\",\"ts\":0}\n{\"agent_id\":%q,\"action\":\"pay\",\"target\":\"alice\",\"amount\":3,\"ts\":18000}\n{\"agent_id\":%q,\"action\":\"pay\",\"target\":\"alice\",\"amount\":5,\"ts\":36000}\n", bob, bob, bob)
	require.NoError(t, os.WriteFile(cfg.Experiment.Trace, []byte(trace), 0o600))
	data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	require.NoError(t, err)
	var template map[string]any
	require.NoError(t, yaml.Unmarshal(data, &template))
	setYAMLPath(template, []string{"system", "shard_num"}, 2)
	setYAMLPath(template, []string{"system", "node_num"}, 4)
	setYAMLPath(template, []string{"system", "consensus_type"}, "static_relay")
	setYAMLPath(template, []string{"system", "log", "log_level"}, "warn")
	setYAMLPath(template, []string{"consensus_node", "block_interval"}, 500)
	setYAMLPath(template, []string{"network", "communication_mode"}, "direct")
	data, err = yaml.Marshal(template)
	require.NoError(t, err)
	cfg.Base.BlockEmulatorConfig = filepath.Join(t.TempDir(), "chain.yaml")
	require.NoError(t, os.WriteFile(cfg.Base.BlockEmulatorConfig, data, 0o600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		var obs Observation
		for _, message := range body.Messages {
			if message.Role == "user" {
				if err := json.Unmarshal([]byte(message.Content), &obs); err != nil {
					http.Error(w, "bad observation", 400)
					return
				}
			}
		}
		decision := Decision{Action: "wait"}
		for _, agent := range obs.Agents {
			if agent.AgentID != "alice" {
				continue
			}
			available, ok := new(big.Int).SetString(agent.AvailableBalance, 10)
			if ok && available.Cmp(big.NewInt(10)) >= 0 {
				decision = Decision{Action: "pay", Target: bob, Amount: "10"}
			}
		}
		content, err := json.Marshal(decision)
		if err != nil {
			http.Error(w, "encoding", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer server.Close()
	cfg.AgentAPI = ModelConfig{Enabled: true, AgentID: "alice", InitialBalance: "2", BaseURL: server.URL, Model: "funding-policy-test", Goal: "wait until enough funds, then pay 10", DecisionTimeoutSeconds: 5, DecisionIntervalSeconds: 1, RunDurationSeconds: 60, DrainTimeoutSeconds: 30}
	require.NoError(t, cfg.AgentAPI.normalize(true))
	chain := &ChainRunner{ModuleRoot: root, BaseConfig: cfg.Base.BlockEmulatorConfig, WorkRoot: cfg.Base.ResultDir, RunTimeout: 150 * time.Second, NodeExitGrace: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, chain.Build(ctx))
	rounds, err := RunHybrid(ctx, cfg, chain)
	require.NoError(t, err)
	data, err = os.ReadFile(filepath.Join(cfg.Base.ResultDir, HybridSummaryFile))
	require.NoError(t, err)
	var summary struct {
		Latest    Observation     `json:"latest_snapshot"`
		Decisions []DecisionAudit `json:"decisions"`
		Pending   int             `json:"pending_transactions"`
	}
	require.NoError(t, json.Unmarshal(data, &summary))
	require.Zero(t, summary.Pending)
	waitedAtTwo, waitedAtFive, waitedAfterPayment := false, false, false
	payments := 0
	for _, audit := range summary.Decisions {
		for _, agent := range audit.Observation.Agents {
			if agent.AgentID != "alice" {
				continue
			}
			require.Contains(t, []string{"2", "5", "10", "0"}, agent.Balance)
			if audit.Reply.Decision.Action == "wait" {
				switch agent.AvailableBalance {
				case "2":
					waitedAtTwo = true
				case "5":
					waitedAtFive = true
				case "0":
					waitedAfterPayment = true
				}
			}
			if audit.Reply.Decision.Action == "pay" && audit.Result == "accepted" {
				require.Equal(t, "10", agent.AvailableBalance)
				require.Equal(t, "10", audit.Reply.Decision.Amount)
				require.NotEmpty(t, audit.TransactionHash)
				payments++
			}
		}
	}
	require.True(t, waitedAtTwo)
	require.True(t, waitedAtFive)
	require.True(t, waitedAfterPayment, "a spent account must remain empty rather than receiving a default top-up")
	require.Equal(t, 1, payments)
	for _, agent := range summary.Latest.Agents {
		if agent.AgentID == "alice" {
			require.Equal(t, "0", agent.Balance)
		}
	}
	file, err := os.Open(filepath.Join(rounds[0].OutputDir, AgentsDirName, "alice.csv"))
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	ledger, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)
	require.Equal(t, "2", ledger[1][5])
	require.Equal(t, "0", ledger[len(ledger)-1][5])
}

func TestHybridTraceFailureIntegration(t *testing.T) {
	if os.Getenv("AGENTEMU_INTEGRATION") != "1" {
		t.Skip("set AGENTEMU_INTEGRATION=1 to launch the trace rejection scenario")
	}
	wd, err := os.Getwd()
	require.NoError(t, err)
	root := filepath.Dir(wd)
	cfg := testConfig(t)
	cfg.Base.ModuleRoot = root
	cfg.Chain.Enabled = true
	cfg.Experiment.Trace = filepath.Join(t.TempDir(), "trace-rejection.jsonl")
	trace := "{\"agent_id\":\"alice\",\"action\":\"join\",\"ts\":0}\n{\"agent_id\":\"bob\",\"action\":\"join\",\"ts\":0}\n{\"agent_id\":\"carol\",\"action\":\"join\",\"ts\":0}\n{\"agent_id\":\"bob\",\"action\":\"pay\",\"target\":\"alice\",\"amount\":3,\"request_id\":\"invalid-after-leave\",\"ts\":20000}\n{\"agent_id\":\"bob\",\"action\":\"pay\",\"target\":\"carol\",\"amount\":4,\"request_id\":\"valid-after-rejection\",\"ts\":26000}\n"
	require.NoError(t, os.WriteFile(cfg.Experiment.Trace, []byte(trace), 0o600))
	data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	require.NoError(t, err)
	var template map[string]any
	require.NoError(t, yaml.Unmarshal(data, &template))
	setYAMLPath(template, []string{"system", "shard_num"}, 2)
	setYAMLPath(template, []string{"system", "node_num"}, 4)
	setYAMLPath(template, []string{"system", "consensus_type"}, "static_relay")
	setYAMLPath(template, []string{"system", "log", "log_level"}, "warn")
	setYAMLPath(template, []string{"consensus_node", "block_interval"}, 500)
	setYAMLPath(template, []string{"network", "communication_mode"}, "direct")
	data, err = yaml.Marshal(template)
	require.NoError(t, err)
	cfg.Base.BlockEmulatorConfig = filepath.Join(t.TempDir(), "chain.yaml")
	require.NoError(t, os.WriteFile(cfg.Base.BlockEmulatorConfig, data, 0o600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"action":"leave"}`}}}})
	}))
	defer server.Close()
	cfg.AgentAPI = ModelConfig{Enabled: true, AgentID: "alice", BaseURL: server.URL, Model: "leave-policy-test", Goal: "leave", DecisionTimeoutSeconds: 5, DecisionIntervalSeconds: 1, RunDurationSeconds: 40, DrainTimeoutSeconds: 30}
	require.NoError(t, cfg.AgentAPI.normalize(true))
	chain := &ChainRunner{ModuleRoot: root, BaseConfig: cfg.Base.BlockEmulatorConfig, WorkRoot: cfg.Base.ResultDir, RunTimeout: 120 * time.Second, NodeExitGrace: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, chain.Build(ctx))
	rounds, err := RunHybrid(ctx, cfg, chain)
	require.NoError(t, err)
	require.Len(t, rounds, 1)
	require.Equal(t, 5, rounds[0].TxCount, "three joins, API leave and the later valid payment")
	data, err = os.ReadFile(filepath.Join(cfg.Base.ResultDir, HybridSummaryFile))
	require.NoError(t, err)
	var summary struct {
		Phase     string          `json:"phase"`
		Failed    int             `json:"trace_actions_failed"`
		Remaining int             `json:"trace_actions_remaining"`
		Pending   int             `json:"pending_transactions"`
		Decisions []DecisionAudit `json:"decisions"`
		Latest    Observation     `json:"latest_snapshot"`
	}
	require.NoError(t, json.Unmarshal(data, &summary))
	require.Equal(t, phaseFinished, summary.Phase)
	require.Equal(t, 1, summary.Failed)
	require.Zero(t, summary.Remaining)
	require.Zero(t, summary.Pending)
	require.Len(t, summary.Decisions, 1)
	require.Equal(t, "leave", summary.Decisions[0].Reply.Decision.Action)
	require.Equal(t, "accepted", summary.Decisions[0].Result)
	data, err = os.ReadFile(filepath.Join(rounds[0].OutputDir, TraceFailuresFile))
	require.NoError(t, err)
	var failure TraceFailure
	require.NoError(t, json.Unmarshal(data, &failure))
	require.Equal(t, 4, failure.TraceLine)
	require.Equal(t, "invalid-after-leave", failure.Record.RequestID)
	require.Contains(t, failure.Reason, "not active")
	data, err = os.ReadFile(filepath.Join(rounds[0].OutputDir, ActionTxMapFileName))
	require.NoError(t, err)
	require.NotContains(t, string(data), "invalid-after-leave")
	require.Contains(t, string(data), "valid-after-rejection")
	initial, ok := new(big.Int).SetString(account.NormalInitBalanceStr, 10)
	require.True(t, ok)
	for _, agent := range summary.Latest.Agents {
		switch agent.AgentID {
		case "alice":
			require.False(t, agent.Active)
			require.Equal(t, initial.String(), agent.Balance)
		case "bob":
			require.Equal(t, new(big.Int).Sub(initial, big.NewInt(4)).String(), agent.Balance)
		case "carol":
			require.Equal(t, new(big.Int).Add(initial, big.NewInt(4)).String(), agent.Balance)
		}
	}
}
