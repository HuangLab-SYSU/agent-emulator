package agentemu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/HuangLab-SYSU/block-emulator-x/supervisor/txsource/plansource"
)

// defaultChainRunTimeout bounds one whole chain run; the supervisor normally
// stops by itself once every planned transaction is on chain and empty blocks
// pile up.
const defaultChainRunTimeout = 10 * time.Minute

// RoundSpec describes one BlockEmulator-X execution of a produced plan.
type RoundSpec struct {
	Round    int
	PlanPath string
	TxCount  int
}

// ChainOutcome points at the artifacts a finished chain run left behind.
type ChainOutcome struct {
	RoundDir  string
	ResultDir string // supervisor measurement CSVs (relay_stats_*.csv, ...)
}

// ChainRunner launches a private BlockEmulator-X cluster (consensus nodes plus
// supervisor) that replays the transaction plan produced by Host. It derives a
// self-contained config and ip table per round, so rounds never share mutable
// state such as bolt/level databases or block records.
type ChainRunner struct {
	// ModuleRoot is the BlockEmulator-X module root (contains go.mod, cmd/, ...).
	ModuleRoot string
	// BaseConfig is the template BlockEmulator-X config (config.yaml).
	BaseConfig string
	// WorkRoot hosts per-round chain outputs (<result_dir>/chain by default).
	WorkRoot string

	// RunTimeout bounds one whole chain run.
	RunTimeout time.Duration
	// NodeExitGrace is how long nodes may take to exit after the supervisor
	// finished before they get killed.
	NodeExitGrace time.Duration
}

// Build compiles the consensusnode and supervisor binaries once for all rounds.
func (c *ChainRunner) Build(ctx context.Context) error {
	binDir := c.binDir()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	for _, target := range []struct{ pkg, name string }{
		{"./cmd/consensusnode", "consensusnode"},
		{"./cmd/supervisor", "supervisor"},
	} {
		binPath := filepath.Join(binDir, target.name)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, target.pkg)

		cmd.Dir = c.ModuleRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("build %s: %w\n%s", target.pkg, err, out)
		}
	}

	return nil
}

func (c *ChainRunner) binDir() string {
	return filepath.Join(c.WorkRoot, "bin")
}

func roundDirName(round int) string {
	return fmt.Sprintf("round_%03d", round)
}

// Run executes one round on a freshly launched cluster and blocks until the
// whole cluster has stopped (or ctx / RunTimeout fires).
func (c *ChainRunner) Run(ctx context.Context, spec RoundSpec) (*ChainOutcome, error) {
	if c.RunTimeout <= 0 {
		c.RunTimeout = defaultChainRunTimeout
	}

	roundDir := filepath.Join(c.WorkRoot, roundDirName(spec.Round))
	if err := os.MkdirAll(roundDir, 0o755); err != nil {
		return nil, fmt.Errorf("create chain round dir: %w", err)
	}

	cfgPath, tablePath, resultDir, err := c.prepare(roundDir, spec)
	if err != nil {
		return nil, err
	}

	shardNum, nodeNum, err := c.clusterShape()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.RunTimeout)
	defer cancel()

	procs := newProcessGroup()
	defer procs.killAll()

	supBin := filepath.Join(c.binDir(), "supervisor")
	nodeBin := filepath.Join(c.binDir(), "consensusnode")

	for shard := int64(0); shard < shardNum; shard++ {
		for node := int64(0); node < nodeNum; node++ {
			p, err := procs.start(nodeBin, roundDir, fmt.Sprintf("node_s%d_n%d", shard, node),
				"-shard_id", strconv.FormatInt(shard, 10),
				"-node_id", strconv.FormatInt(node, 10),
				"-config", cfgPath,
				"-ip_table", tablePath,
			)
			if err != nil {
				return nil, err
			}

			slog.Info("started consensus node", "shard", shard, "node", node, "pid", p.Pid)
		}
	}

	sup, err := procs.start(supBin, roundDir, "supervisor",
		"-shard_id", "2147483647",
		"-node_id", "0",
		"-config", cfgPath,
		"-ip_table", tablePath,
	)
	if err != nil {
		return nil, err
	}

	slog.Info("started supervisor", "pid", sup.Pid, "round", spec.Round)

	// Nodes sleep 5s and the supervisor 8s before their main loops start; the
	// cluster then runs until the supervisor's stop logic fires.
	supErr := procs.wait(sup, ctx.Done())

	// The supervisor broadcast StopConsensusMsg on exit; give the nodes a grace
	// window to follow before killing them.
	procs.waitGrace(c.graceDuration(), ctx.Done())

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("chain run for round %d aborted: %w (logs in %s)", spec.Round, err, roundDir)
	}

	if supErr != nil {
		return nil, fmt.Errorf("supervisor exited with error: %w (logs in %s)", supErr, roundDir)
	}

	return &ChainOutcome{RoundDir: roundDir, ResultDir: resultDir}, nil
}

func (c *ChainRunner) graceDuration() time.Duration {
	if c.NodeExitGrace > 0 {
		return c.NodeExitGrace
	}

	return 15 * time.Second
}

// prepare writes the derived config.yaml and ip_table.json for one round and
// returns their paths plus the supervisor result directory.
func (c *ChainRunner) prepare(roundDir string, spec RoundSpec) (string, string, string, error) {
	doc, err := c.loadBaseConfig()
	if err != nil {
		return "", "", "", err
	}

	planPath, err := filepath.Abs(spec.PlanPath)
	if err != nil {
		return "", "", "", fmt.Errorf("abs plan path: %w", err)
	}

	resultDir := filepath.Join(roundDir, "results")

	// Point every mutable path at the round directory and switch the supervisor
	// to replaying the produced plan.
	setYAMLPath(doc, []string{"system", "log", "log_dir"}, roundDir)
	setYAMLPath(
		doc,
		[]string{"consensus_node", "blockchain", "storage", "bolt", "file_path_dir"},
		filepath.Join(roundDir, "boltdb"),
	)
	setYAMLPath(
		doc,
		[]string{"consensus_node", "blockchain", "storage", "eth_storage", "level_file_path_dir"},
		filepath.Join(roundDir, "trie_db"),
	)
	setYAMLPath(doc, []string{"consensus_node", "block_record_dir"}, filepath.Join(roundDir, "block_record"))
	setYAMLPath(doc, []string{"supervisor", "result_output_dir"}, resultDir)
	setYAMLPath(doc, []string{"supervisor", "tx_source", "tx_source_type"}, plansource.Key)
	setYAMLPath(doc, []string{"supervisor", "tx_source", "tx_source_file"}, planPath)
	setYAMLPath(doc, []string{"supervisor", "tx_source", "exclude_contract_txs"}, false)
	// The supervisor stops once tx_number transactions are injected and empty
	// blocks pile up; pinning it to the plan size lets the run finish by itself.
	setYAMLPath(doc, []string{"supervisor", "tx_number"}, spec.TxCount)

	cfgBytes, err := yaml.Marshal(doc)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal derived blockemulator config: %w", err)
	}

	cfgPath := filepath.Join(roundDir, "config.yaml")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o644); err != nil {
		return "", "", "", fmt.Errorf("write derived blockemulator config: %w", err)
	}

	shardNum, nodeNum, err := c.clusterShape()
	if err != nil {
		return "", "", "", err
	}

	tablePath := filepath.Join(roundDir, "ip_table.json")
	if err := writeIPTable(tablePath, shardNum, nodeNum); err != nil {
		return "", "", "", err
	}

	return cfgPath, tablePath, resultDir, nil
}

func (c *ChainRunner) loadBaseConfig() (map[string]any, error) {
	raw, err := os.ReadFile(c.BaseConfig)
	if err != nil {
		return nil, fmt.Errorf("read base blockemulator config: %w", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse base blockemulator config: %w", err)
	}

	return doc, nil
}

// clusterShape reads shard_num / node_num from the base config.
func (c *ChainRunner) clusterShape() (int64, int64, error) {
	doc, err := c.loadBaseConfig()
	if err != nil {
		return 0, 0, err
	}

	shardNum, err := yamlInt(doc, "system", "shard_num")
	if err != nil {
		return 0, 0, err
	}

	nodeNum, err := yamlInt(doc, "system", "node_num")
	if err != nil {
		return 0, 0, err
	}

	if shardNum <= 0 || nodeNum <= 0 {
		return 0, 0, fmt.Errorf("system.shard_num and system.node_num must be positive in %s", c.BaseConfig)
	}

	return shardNum, nodeNum, nil
}

// writeIPTable generates a loopback ip table matching the layout of the
// repository's checked-in ip_table.json (ports 322xx per shard, 38800 for the
// supervisor shard 0x7fffffff = 2147483647).
func writeIPTable(path string, shardNum, nodeNum int64) error {
	const (
		nodePortBase = 32207 // + shard*100 + (node+1)*10
		supervisorEP = "127.0.0.1:38800"
	)

	table := make(map[string]map[string]string, shardNum+1)
	for shard := int64(0); shard < shardNum; shard++ {
		nodes := make(map[string]string, nodeNum)
		for node := int64(0); node < nodeNum; node++ {
			port := nodePortBase + shard*100 + (node+1)*10
			nodes[strconv.FormatInt(node, 10)] = fmt.Sprintf("127.0.0.1:%d", port)
		}

		table[strconv.FormatInt(shard, 10)] = nodes
	}

	table["2147483647"] = map[string]string{"0": supervisorEP}

	b, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ip table: %w", err)
	}

	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write ip table: %w", err)
	}

	return nil
}

func setYAMLPath(doc map[string]any, path []string, value any) {
	m := doc
	for _, key := range path[:len(path)-1] {
		next, ok := m[key].(map[string]any)
		if !ok {
			next = make(map[string]any)
			m[key] = next
		}

		m = next
	}

	m[path[len(path)-1]] = value
}

func yamlInt(doc map[string]any, path ...string) (int64, error) {
	var cur any = doc
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("config path %v is missing", path)
		}

		cur, ok = m[key]
		if !ok {
			return 0, fmt.Errorf("config key %v not found", path)
		}
	}

	switch v := cur.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case uint64:
		return int64(v), nil
	case float64:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("config key %v is not a number: %T", path, cur)
	}
}

// processGroup tracks spawned cluster processes so every one of them is
// reaped, even on error paths and Ctrl-C.
type processGroup struct {
	procs []*os.Process
	done  map[int]chan error
}

func newProcessGroup() *processGroup {
	return &processGroup{done: make(map[int]chan error)}
}

// start launches one cluster process. Its output is teed: every line lands
// in the per-process log file AND on the agentemu console, mirroring what a
// direct example_run.sh launch would print.
func (g *processGroup) start(bin, logDir, name string, args ...string) (*os.Process, error) {
	logFile, err := os.Create(filepath.Join(logDir, name+".log"))
	if err != nil {
		return nil, fmt.Errorf("create log for %s: %w", name, err)
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdout = io.MultiWriter(logFile, os.Stdout)
	cmd.Stderr = io.MultiWriter(logFile, os.Stderr)

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}

	g.procs = append(g.procs, cmd.Process)
	g.done[cmd.Process.Pid] = make(chan error, 1)

	go func() {
		defer func() { _ = logFile.Close() }()

		g.done[cmd.Process.Pid] <- cmd.Wait()
	}()

	return cmd.Process, nil
}

// wait blocks until the given process exits or abort fires.
func (g *processGroup) wait(p *os.Process, abort <-chan struct{}) error {
	ch, ok := g.done[p.Pid]
	if !ok {
		return fmt.Errorf("unknown pid %d", p.Pid)
	}

	select {
	case err := <-ch:
		return err
	case <-abort:
		return fmt.Errorf("aborted before exit: %w", context.Canceled)
	}
}

// waitGrace waits for all remaining processes to exit on their own; when the
// grace window passes (or abort fires) the leftovers are killed.
func (g *processGroup) waitGrace(grace time.Duration, abort <-chan struct{}) {
	deadline := time.NewTimer(grace)
	defer deadline.Stop()

	for {
		alive := 0

		for pid, ch := range g.done {
			select {
			case <-ch:
				delete(g.done, pid)
			default:
				alive++
			}
		}

		if alive == 0 {
			return
		}

		select {
		case <-deadline.C:
			slog.Warn("killing cluster processes that did not exit in time", "alive", alive)
			g.killAll()

			return
		case <-abort:
			g.killAll()
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (g *processGroup) killAll() {
	for _, p := range g.procs {
		_ = p.Kill()
	}
}
