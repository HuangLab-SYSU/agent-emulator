package agentemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RoundSummaryFile is written at the result root when a run finishes.
const RoundSummaryFile = "rounds_summary.json"

// RoundResult summarizes one finished simulation round. It is the payload the
// reserved AgentAPI feedback hook receives.
type RoundResult struct {
	Round       int `json:"round"`
	RecordCount int `json:"record_count"`
	TxCount     int `json:"tx_count"`

	OutputDir string `json:"output_dir"`
	PlanPath  string `json:"plan_path"`
	// ChainResultDir points at the supervisor measurement CSVs of this round;
	// empty when chain execution is disabled.
	ChainResultDir string `json:"chain_result_dir,omitempty"`
}

// AgentAPI is the feedback hook reserved for the multi-round loop: it receives
// the result of the finished round (including where the chain outputs live)
// and returns the next trace. Returning an empty slice ends the loop.
//
// The initial release ships no implementation beyond NoFeedback; a real Agent
// API (e.g. an HTTP service or an LLM-backed driver) plugs in here without
// touching the loop itself.
type AgentAPI interface {
	NextTrace(ctx context.Context, prev RoundResult) ([]Record, error)
}

// EndCondition decides after each round whether the simulation loop stops.
type EndCondition interface {
	ShouldStop(prev RoundResult) bool
}

// NoFeedback implements AgentAPI for plan-only runs: there is never a next
// trace, so the loop runs exactly one round (unless an EndCondition says
// otherwise).
type NoFeedback struct{}

func (NoFeedback) NextTrace(context.Context, RoundResult) ([]Record, error) {
	return nil, nil
}

// FixedRounds implements EndCondition by capping the loop at n rounds.
type FixedRounds int

func (n FixedRounds) ShouldStop(prev RoundResult) bool {
	return prev.Round >= int(n)
}

// Runner drives the simulation loop: trace -> transaction plan -> (optional)
// BlockEmulator-X execution, repeated while the AgentAPI keeps producing
// traces and the EndCondition allows it.
type Runner struct {
	Cfg Config
	// Records is the initial trace.
	Records []Record
	// AgentAPI is nil-safe; nil means NoFeedback.
	AgentAPI AgentAPI
	// End is nil-safe; nil means FixedRounds(Cfg.Loop.MaxRounds).
	End EndCondition
	// Chain is optional; nil skips BlockEmulator-X execution (plan only).
	Chain *ChainRunner
}

// Run executes the loop and returns the per-round results. The agent registry
// is shared across rounds, so agent DIDs stay stable from round to round.
func (r *Runner) Run(ctx context.Context) ([]RoundResult, error) {
	agentAPI := r.AgentAPI
	if agentAPI == nil {
		agentAPI = NoFeedback{}
	}

	end := r.End
	if end == nil {
		maxRounds := r.Cfg.Loop.MaxRounds
		if maxRounds < 1 {
			maxRounds = 1
		}

		end = FixedRounds(maxRounds)
	}

	records := r.Records
	rounds := make([]RoundResult, 0, 1)

	for round := 1; ; round++ {
		host, err := NewHost(r.Cfg)
		if err != nil {
			return rounds, fmt.Errorf("round %d: create host: %w", round, err)
		}

		result, err := host.Process(records)
		if err != nil {
			return rounds, fmt.Errorf("round %d: %w", round, err)
		}

		outDir := filepath.Join(r.Cfg.Base.ResultDir, roundDirName(round))
		if err := host.WriteResult(outDir, result); err != nil {
			return rounds, fmt.Errorf("round %d: %w", round, err)
		}

		rr := RoundResult{
			Round:       round,
			RecordCount: len(records),
			TxCount:     len(result.Transactions),
			OutputDir:   outDir,
			PlanPath:    filepath.Join(outDir, PlanFileName),
		}

		if r.Chain != nil {
			outcome, err := r.Chain.Run(ctx, RoundSpec{Round: round, PlanPath: rr.PlanPath, TxCount: rr.TxCount})
			if err != nil {
				return rounds, fmt.Errorf("round %d: %w", round, err)
			}

			rr.ChainResultDir = outcome.ResultDir

			// One CSV per agent: every committed transaction it took part in,
			// read back from the shards' block storages.
			agentsDir := filepath.Join(outDir, AgentsDirName)
			if err := WriteAgentCSVs(ctx, outcome.ChainDir, outcome.ShardNum, host.registry, agentsDir); err != nil {
				return rounds, fmt.Errorf("round %d: %w", round, err)
			}
		}

		rounds = append(rounds, rr)

		if end.ShouldStop(rr) {
			break
		}

		next, err := agentAPI.NextTrace(ctx, rr)
		if err != nil {
			return rounds, fmt.Errorf("round %d: agent api: %w", round, err)
		}

		if len(next) == 0 {
			break
		}

		records = next
	}

	if err := writeRoundsSummary(r.Cfg.Base.ResultDir, r.Cfg, rounds); err != nil {
		return rounds, err
	}

	return rounds, nil
}

func writeRoundsSummary(dir string, cfg Config, rounds []RoundResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}

	summary := struct {
		Seed     int64         `json:"seed"`
		Trace    string        `json:"trace"`
		RoundNum int           `json:"round_num"`
		Rounds   []RoundResult `json:"rounds"`
	}{Seed: cfg.Experiment.Seed, Trace: cfg.Experiment.Trace, RoundNum: len(rounds), Rounds: rounds}

	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rounds summary: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, RoundSummaryFile), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write rounds summary: %w", err)
	}

	return nil
}
