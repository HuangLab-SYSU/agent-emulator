package agentsupervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/HuangLab-SYSU/block-emulator-x/config"
	"github.com/HuangLab-SYSU/block-emulator-x/supervisor/txsource/tracesource"
)

const baseConfigFixture = `system:
  shard_num: 2
  node_num: 4
  consensus_type: "static_relay"
  log:
    log_dir: "./exp/"
consensus_node:
  blockchain:
    storage:
      block_storage_type: "bolt"
      bolt:
        file_path_dir: "./exp/boltdb/"
      eth_storage:
        level_file_path_dir: "./exp/trie_db/"
  block_record_dir: "./exp/block_record/"
supervisor:
  tx_number: 100000
  result_output_dir: "./exp/results/"
  tx_source:
    tx_source_type: "random_source"
    tx_source_file: ""
network:
  communication_mode: "direct"
`

func newTestChainRunner(t *testing.T) *ChainRunner {
	t.Helper()

	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(base, []byte(baseConfigFixture), 0o644))

	return &ChainRunner{ModuleRoot: dir, BaseConfig: base, WorkRoot: filepath.Join(dir, "results")}
}

func TestChainRunnerPrepareDerivesConfigAndIPTable(t *testing.T) {
	c := newTestChainRunner(t)

	chainDir := filepath.Join(c.WorkRoot, roundDirName(1), "chain")
	require.NoError(t, os.MkdirAll(chainDir, 0o755))

	planPath := filepath.Join(c.WorkRoot, roundDirName(1), PlanFileName)
	require.NoError(t, os.WriteFile(planPath, []byte("{}\n"), 0o644))

	cfgPath, tablePath, resultDir, err := c.prepare(chainDir, RoundSpec{Round: 1, PlanPath: planPath, TxCount: 7})
	require.NoError(t, err)

	raw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)

	var derived map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &derived))

	require.Equal(t, tracesource.Key, derived["supervisor"].(map[string]any)["tx_source"].(map[string]any)["tx_source_type"])

	absPlan, err := filepath.Abs(planPath)
	require.NoError(t, err)
	require.Equal(t, absPlan, derived["supervisor"].(map[string]any)["tx_source"].(map[string]any)["tx_source_file"])

	// tx_number is pinned to the plan size so the supervisor can stop.
	require.EqualValues(t, 7, derived["supervisor"].(map[string]any)["tx_number"])

	// Mutable paths are redirected into the chain dir's logs/data/results
	// subdirectories; network settings are kept.
	require.Equal(t, filepath.Join(chainDir, "logs"), derived["system"].(map[string]any)["log"].(map[string]any)["log_dir"])
	require.Equal(t, filepath.Join(chainDir, "data", "boltdb"), derived["consensus_node"].(map[string]any)["blockchain"].(map[string]any)["storage"].(map[string]any)["bolt"].(map[string]any)["file_path_dir"])
	require.Equal(t, filepath.Join(chainDir, "data", "trie_db"), derived["consensus_node"].(map[string]any)["blockchain"].(map[string]any)["storage"].(map[string]any)["eth_storage"].(map[string]any)["level_file_path_dir"])
	require.Equal(t, filepath.Join(chainDir, "data", "block_record"), derived["consensus_node"].(map[string]any)["block_record_dir"])
	require.Equal(t, filepath.Join(chainDir, "results"), resultDir)
	require.Equal(t, resultDir, derived["supervisor"].(map[string]any)["result_output_dir"])
	require.Equal(t, "direct", derived["network"].(map[string]any)["communication_mode"])

	// IP table covers every shard/node plus the supervisor entry.
	tableRaw, err := os.ReadFile(tablePath)
	require.NoError(t, err)

	var table map[string]map[string]string
	require.NoError(t, json.Unmarshal(tableRaw, &table))

	require.Len(t, table, 3)
	require.Equal(t, "127.0.0.1:32217", table["0"]["0"])
	require.Equal(t, "127.0.0.1:32247", table["0"]["3"])
	require.Equal(t, "127.0.0.1:32347", table["1"]["3"])
	require.Equal(t, "127.0.0.1:38800", table["2147483647"]["0"])
}

func TestChainRunnerClusterShape(t *testing.T) {
	c := newTestChainRunner(t)

	shardNum, nodeNum, err := c.clusterShape()
	require.NoError(t, err)
	require.EqualValues(t, 2, shardNum)
	require.EqualValues(t, 4, nodeNum)
}

func TestProcessGroupReapedProcessIsNotWaitedTwice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	g := newProcessGroup()
	defer g.killAll()
	p, err := g.start("/bin/sh", t.TempDir(), "exit", "-c", "exit 0")
	require.NoError(t, err)
	require.NoError(t, g.wait(p, context.Background().Done()))
	require.NotContains(t, g.done, p.Pid, "a consumed completion channel must not remain in grace wait")
}

func TestChainRunnerOnlineModeSelection(t *testing.T) {
	c := newTestChainRunner(t)
	c.OnlineConfigPath = "/runtime/agent.yaml"
	dir := t.TempDir()
	path, _, _, err := c.prepare(dir, RoundSpec{Round: 1, PlanPath: filepath.Join(dir, "plan.jsonl")})
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc))
	require.Equal(t, c.OnlineConfigPath, doc["system"].(map[string]any)["agent_runtime_config"])
	require.NoError(t, os.WriteFile(c.BaseConfig, []byte("system:\n  consensus_type: static_broker\n"), 0o600))
	_, _, _, err = c.prepare(t.TempDir(), RoundSpec{})
	require.ErrorContains(t, err, "static_relay")
}

func TestChainRunnerPassesInitialBalances(t *testing.T) {
	c := newTestChainRunner(t)
	c.InitialBalances = map[string]string{"0x0000000000000000000000000000000000000001": "2"}
	dir := t.TempDir()
	path, _, _, err := c.prepare(dir, RoundSpec{Round: 1, PlanPath: filepath.Join(dir, "plan.jsonl")})
	require.NoError(t, err)
	cfg, err := config.LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, c.InitialBalances, cfg.GlobalSys.InitialBalances)
}
