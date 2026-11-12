package agentsupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RunHybrid starts exactly one cluster for the whole trace/API experiment.
// AgentSupervisor calls the model while the child supervisor runs the chain.
func RunHybrid(ctx context.Context, cfg Config, chain *ChainRunner) ([]RoundResult, error) {
	if chain == nil {
		return nil, fmt.Errorf("hybrid runtime requires a chain runner")
	}

	records, err := LoadTrace(cfg.Experiment.Trace)
	if err != nil {
		return nil, err
	}

	if _, _, err := FilterHybridTrace(records, cfg.AgentAPI.AgentID, cfg.Experiment.Seed); err != nil {
		return nil, err
	}

	for _, target := range []*string{&cfg.Base.ResultDir, &cfg.Base.ModuleRoot, &cfg.Base.BlockEmulatorConfig, &cfg.Experiment.Trace} {
		absolute, err := filepath.Abs(*target)
		if err != nil {
			return nil, fmt.Errorf("resolve hybrid path: %w", err)
		}

		*target = absolute
	}

	entries, err := os.ReadDir(cfg.Base.ResultDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect hybrid result directory: %w", err)
	}

	if len(entries) != 0 {
		return nil, fmt.Errorf("hybrid runtime requires an empty result_dir")
	}

	dir := filepath.Join(cfg.Base.ResultDir, roundDirName(1))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create hybrid output: %w", err)
	}

	runtimeConfig := filepath.Join(cfg.Base.ResultDir, "hybrid_config.yaml")

	agent, err := NewAgentSupervisor(cfg)
	if err != nil {
		return nil, err
	}

	endpoint, closeDecisions, err := agent.serveDecisions(ctx)
	if err != nil {
		return nil, err
	}

	defer closeDecisions()

	childConfig := cfg
	childConfig.DecisionEndpoint = endpoint
	childConfig.AgentAPI.APIKeyEnv = ""
	childConfig.AgentAPI.APIKeyFile = ""

	data, err := yaml.Marshal(childConfig)
	if err != nil {
		return nil, fmt.Errorf("encode runtime config: %w", err)
	}

	if err := os.WriteFile(runtimeConfig, data, 0o600); err != nil {
		return nil, fmt.Errorf("write runtime config: %w", err)
	}

	if cfg.AgentAPI.InitialBalance != "" {
		address, err := didAddress(allocatedDID(cfg.Experiment.Seed, cfg.AgentAPI.AgentID))
		if err != nil {
			return nil, err
		}

		chain.InitialBalances = map[string]string{fmt.Sprintf("0x%x", address): cfg.AgentAPI.InitialBalance}
	}

	chain.OnlineConfigPath = runtimeConfig
	chain.WorkRoot = cfg.Base.ResultDir

	plan := filepath.Join(dir, PlanFileName)
	if _, err := os.Stat(plan); err == nil {
		return nil, fmt.Errorf("hybrid result directory already contains a plan; choose a fresh result_dir")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("check hybrid plan: %w", err)
	}

	if err := os.WriteFile(plan, nil, 0o600); err != nil {
		return nil, fmt.Errorf("create initial hybrid plan: %w", err)
	}

	outcome, err := chain.Run(ctx, RoundSpec{Round: 1, PlanPath: plan})
	if err != nil {
		return nil, err
	}

	registry, err := LoadRegistry(filepath.Join(cfg.Base.ResultDir, RegistryFileName), cfg.Experiment.Seed)
	if err != nil {
		return nil, err
	}

	if err := WriteAgentCSVs(
		ctx,
		outcome.ChainDir,
		outcome.ShardNum,
		registry,
		filepath.Join(dir, AgentsDirName),
	); err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(filepath.Join(cfg.Base.ResultDir, HybridSummaryFile))
	if err != nil {
		return nil, fmt.Errorf("read hybrid summary: %w", err)
	}

	var summary struct {
		PendingTransactions int    `json:"pending_transactions"`
		Phase               string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return nil, fmt.Errorf("decode hybrid summary: %w", err)
	}

	if summary.Phase != phaseFinished || summary.PendingTransactions != 0 {
		return nil, fmt.Errorf("hybrid runtime did not drain cleanly")
	}

	compiled, err := LoadTrace(filepath.Join(dir, "input_actions.jsonl"))
	if err != nil {
		return nil, err
	}
	// Plans have one line per transaction, while idempotent joins produce none.
	planData, err := os.ReadFile(plan)
	if err != nil {
		return nil, fmt.Errorf("read hybrid plan: %w", err)
	}

	txCount := 0

	for _, b := range planData {
		if b == '\n' {
			txCount++
		}
	}

	rounds := []RoundResult{
		{
			Round:          1,
			RecordCount:    len(compiled),
			TxCount:        txCount,
			OutputDir:      dir,
			PlanPath:       plan,
			ChainResultDir: outcome.ResultDir,
		},
	}

	return rounds, writeRoundsSummary(cfg.Base.ResultDir, cfg, rounds)
}
