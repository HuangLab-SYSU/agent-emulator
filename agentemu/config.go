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
	} `yaml:"base"`
	Experiment struct {
		Seed  int64  `yaml:"seed"`
		Trace string `yaml:"trace"`
	} `yaml:"experiment"`
	Protocols struct {
		Pay struct {
			Plugin          string `yaml:"plugin"`
			ContractAddress string `yaml:"contract_address"`
		} `yaml:"pay"`
		Audit struct {
			Plugin          string `yaml:"plugin"`
			ContractAddress string `yaml:"contract_address"`
			BatchSize       int    `yaml:"batch_size"`
		} `yaml:"audit"`
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
	if cfg.Protocols.Pay.Plugin == "" || cfg.Protocols.Audit.Plugin == "" || cfg.Protocols.Identity.Plugin == "" {
		return Config{}, fmt.Errorf("a plugin must be selected for pay, audit, and identity")
	}
	if cfg.Protocols.Pay.Plugin != "direct-pay" {
		return Config{}, fmt.Errorf("initial release only supports protocols.pay.plugin=direct-pay")
	}
	if cfg.Protocols.Audit.BatchSize <= 0 {
		cfg.Protocols.Audit.BatchSize = 1
	}

	return cfg, nil
}
