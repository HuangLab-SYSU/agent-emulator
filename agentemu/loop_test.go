package agentemu

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubAgentAPI simulates the reserved feedback hook: it hands out one extra
// trace after the first round and nothing afterwards.
type stubAgentAPI struct {
	calls int
	next  []Record
}

func (s *stubAgentAPI) NextTrace(_ context.Context, _ RoundResult) ([]Record, error) {
	s.calls++
	if s.calls == 1 {
		return s.next, nil
	}

	return nil, nil
}

func TestRunnerLoopProducesPerRoundOutputAndStableDIDs(t *testing.T) {
	cfg := testConfig(t)

	runner := &Runner{
		Cfg: cfg,
		Records: []Record{
			{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
			{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b", TS: 2, Seq: 2},
			{AgentID: "bob", Action: ActionLeave, ParamsHash: "exit-b", TS: 3, Seq: 3},
		},
		AgentAPI: &stubAgentAPI{next: []Record{
			{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a2", TS: 1, Seq: 1},
			{AgentID: "bob", Action: ActionJoin, ParamsHash: "doc-b2", TS: 2, Seq: 2},
			{AgentID: "alice", Target: "bob", Action: ActionPay, Amount: 5, TS: 3, Seq: 3},
		}},
	}

	rounds, err := runner.Run(context.Background())
	require.NoError(t, err)
	require.Len(t, rounds, 2)
	require.Equal(t, 1, rounds[0].Round)
	require.Equal(t, 2, rounds[1].Round)
	require.Empty(t, rounds[0].ChainResultDir) // chain execution disabled in this runner

	// Per-round plans exist.
	for _, rr := range rounds {
		require.FileExists(t, rr.PlanPath)
		require.FileExists(t, filepath.Join(rr.OutputDir, MetricsFileName))
		require.FileExists(t, filepath.Join(rr.OutputDir, ActionTxMapFileName))
	}

	// Identity continuity: round 2 re-joins alice who was already active, so
	// only bob's register appears in round 2's events.
	events, err := os.ReadFile(filepath.Join(rounds[1].OutputDir, MetricsFileName))
	require.NoError(t, err)
	require.Contains(t, string(events), "did_register")
	require.Equal(t, 1, countEventKind(string(events), "did_register"))

	// Shared registry keeps one entry per agent across rounds.
	registry, err := LoadRegistry(filepath.Join(cfg.Base.ResultDir, RegistryFileName), cfg.Experiment.Seed)
	require.NoError(t, err)
	alice, err := registry.Active("alice")
	require.NoError(t, err)
	require.Equal(t, allocatedDID(cfg.Experiment.Seed, "alice"), alice.DID)

	// Summary file lists both rounds.
	summaryRaw, err := os.ReadFile(filepath.Join(cfg.Base.ResultDir, RoundSummaryFile))
	require.NoError(t, err)

	var summary struct {
		RoundNum int           `json:"round_num"`
		Rounds   []RoundResult `json:"rounds"`
	}
	require.NoError(t, json.Unmarshal(summaryRaw, &summary))
	require.Equal(t, 2, summary.RoundNum)
	require.Len(t, summary.Rounds, 2)
}

func TestRunnerSingleRoundWithoutAgentAPI(t *testing.T) {
	cfg := testConfig(t)

	runner := &Runner{
		Cfg: cfg,
		Records: []Record{
			{AgentID: "alice", Action: ActionJoin, ParamsHash: "doc-a", TS: 1, Seq: 1},
		},
	}

	rounds, err := runner.Run(context.Background())
	require.NoError(t, err)
	require.Len(t, rounds, 1)
}

func countEventKind(csvContent, kind string) int {
	count := 0
	lineStart := 0

	for i := 0; i <= len(csvContent); i++ {
		if i == len(csvContent) || csvContent[i] == '\n' {
			line := csvContent[lineStart:i]
			if len(line) > len(kind) && line[:len(kind)] == kind {
				count++
			}

			lineStart = i + 1
		}
	}

	return count
}
