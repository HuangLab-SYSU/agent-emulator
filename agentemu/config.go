package agentemu

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is intentionally separate from BlockEmulator-X's config.yaml so legacy
// experiments retain their existing configuration and execution paths.
type Config struct {
	Base struct {
		BlockEmulatorConfig string `yaml:"blockemulator_config"`
		ResultDir           string `yaml:"result_dir"`
		// ModuleRoot is the BlockEmulator-X module root used to build and launch
		// the consensus nodes and the supervisor. Empty defaults to the working dir.
		ModuleRoot string `yaml:"module_root"`
	} `yaml:"base"`
	Experiment struct {
		Seed  int64  `yaml:"seed"`
		Trace string `yaml:"trace"`
	} `yaml:"experiment"`
	Chain struct {
		// Enabled turns on automatic BlockEmulator-X execution after each round
		// produces its transaction plan.
		Enabled bool `yaml:"enabled"`
		// RunTimeoutSeconds bounds one whole chain run (default 600).
		RunTimeoutSeconds int `yaml:"run_timeout_seconds"`
		// NodeExitGraceSeconds is how long consensus nodes may take to exit after
		// the supervisor finished (default 15).
		NodeExitGraceSeconds int `yaml:"node_exit_grace_seconds"`
	} `yaml:"chain"`
	Loop struct {
		// MaxRounds caps the simulation loop even if an AgentAPI keeps producing
		// traces (default 1).
		MaxRounds int `yaml:"max_rounds"`
	} `yaml:"loop"`
	Protocols struct {
		Pay struct {
			Plugin          string `yaml:"plugin"`
			ContractAddress string `yaml:"contract_address"`
		} `yaml:"pay"`
		Identity struct {
			Plugin          string `yaml:"plugin"`
			ContractAddress string `yaml:"contract_address"`
		} `yaml:"identity"`
	} `yaml:"protocols"`
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read agentemu config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal agentemu config: %w", err)
	}

	if cfg.Base.ResultDir == "" || cfg.Experiment.Trace == "" {
		return Config{}, fmt.Errorf("base.result_dir and experiment.trace are required")
	}

	if cfg.Base.ModuleRoot == "" {
		cfg.Base.ModuleRoot = "."
	}

	if cfg.Chain.Enabled && cfg.Base.BlockEmulatorConfig == "" {
		return Config{}, fmt.Errorf("base.blockemulator_config is required when chain.enabled is true")
	}

	if cfg.Chain.RunTimeoutSeconds <= 0 {
		cfg.Chain.RunTimeoutSeconds = 600
	}

	if cfg.Chain.NodeExitGraceSeconds <= 0 {
		cfg.Chain.NodeExitGraceSeconds = 15
	}

	if cfg.Loop.MaxRounds <= 0 {
		cfg.Loop.MaxRounds = 1
	}

	if cfg.Protocols.Pay.Plugin == "" || cfg.Protocols.Identity.Plugin == "" {
		return Config{}, fmt.Errorf("a plugin must be selected for pay and identity")
	}

	if cfg.Protocols.Pay.Plugin != "direct-pay" {
		return Config{}, fmt.Errorf("initial release only supports protocols.pay.plugin=direct-pay")
	}

	return cfg, nil
}
