# AGENTS.md

AgentEmulator (v1.0): a simulation & experiment platform for trustworthy AI-agent
infrastructure by HuangLab @ SYSU, built on top of BlockEmulator-X (module
`github.com/HuangLab-SYSU/block-emulator-x`, Go 1.25.7). Experiments are described
as JSONL **traces** of agent behavior (`join` / `pay` / `leave` + plain transfers);
the **agentSupervisor** compiles a trace into a transaction dataset, launches a
fresh BlockEmulator-X chain, replays the transactions, records per-agent ledgers,
and plots balance figures into a bilingual HTML gallery.

## How an experiment runs
- Entry point `bash run_agentemu.sh` (Windows: `run_agentemu.bat`): `go build ./...`
  → wipe `./exp` → run `cmd/agentemu` → auto-plot → open `figs/figs_results/index.html`.
- `cmd/agentemu/main.go` loads `agentEmuConfig.yaml`, then `agentsupervisor` drives
  the pipeline: `trace.go` (parse/validate trace, derive DIDs from `seed`+`agent_id`,
  nonces, data fields) → `registry.go` (`agent_registry.json`, `active` state) →
  `chainrunner.go` (derive a per-run `config.yaml`/`ip_table.json` from the template,
  set `tx_source=plan_source` + `tx_number`, start consensus nodes + supervisor,
  wait for clean stop) → `agentcsv.go` (per-agent ledgers) → `loop.go` (rounds; v1.0
  defaults to a single round — multi-round AgentAPI/EndCondition feedback is reserved).
- Underlying chain layer (BlockEmulator-X): `consensus/pbft` (PBFT; node 0 per shard
  is leader), `pkg/core` (Account/Block/Transaction/TxPool), `pkg/chain`, `pkg/vm` +
  `pkg/contractexec` (EVM), `pkg/partition` (CLPA), `supervisor/`. Storage = 3 stores
  (`pkg/storage`): BoltDB block store, go-ethereum `StateDB` world state, account-location
  MPT. Blocks are `TxBlock` or `MigrationBlock` (exactly one of `Body`/`MigrationOpt`).

## Trace rules (enforced; violations abort the run with line numbers)
- Each line: one agent action or one plain transfer. `ts` sets logical order (ties
  break by file order); `ts` is not on-chain time. Plain-transfer lines
  (`{"sender","recipient","value"}`) have no `action` field and inherit the previous
  line's `ts`.
- `pay`: both `agent_id` and `target` must be `active` (joined, not left); `amount`
  must be a positive integer; `request_id` should be globally unique (links intent →
  tx hashes → ledgers); `params_hash` is version metadata, written through as-is.
- `join`/`leave` compile to DID `register`/`revoke` contract calls (record-only in
  v1.0 — the DID contract is not deployed); `pay` compiles to a real balance transfer.
  Never write DIDs into traces — they are derived deterministically from `seed`.

## Config: two layers
- `agentEmuConfig.yaml` (agent side): `experiment.seed/trace`, `chain.enabled`
  (false = compile-only preview, no chain), `chain.run_timeout_seconds`,
  `loop.max_rounds`, `protocols.{pay,identity}` plugins.
- `config.yaml` (BlockEmulator-X template): `system.shard_num`/`node_num` (default
  4×4), `consensus_node.block_interval`, `system.log.log_level`, consensus type
  (`static_relay`, `static_broker`, `clpa_relay`, `clpa_broker`), network mode
  (`direct`/`libp2p`). When changing shard/node counts, also update `ip_table.json`.

## Outputs & analysis
- `exp/agentemu-results/`: `agent_registry.json`, `rounds_summary.json`,
  `round_001/{agent_transactions.jsonl, agent_action_txs.jsonl, Agent_Events.csv,
  agents/agent-XXX.csv, chain/{logs/, results/relay_stats_*.csv}}`.
- Agent CSV columns: `block_height, tx_hash, sender, recipient, value, balance,
  block_time_ms`. `balance` ≈ 10^36 — exceeds int64, always read as string.
- Confirmation analysis joins `agent_action_txs.jsonl` (request_id → tx hashes) with
  `relay_stats_detail_tx_info.csv` (hash → commit times); latency =
  `Tx finally commit time − Tx create time`.
- Plotting: `figs/python_code/plot_agent_balance.py` + `build_fig_html.py`; rerun
  manually with `--data-dir exp/agentemu-results/round_XXX/agents` and `--fig-dir`.
  Both `exp/` and `figs/figs_results/` are wiped on every run — back up before reruns.
- Generate test traces with `scripts/gen_pay_trace.py`; samples live in `traces/`
  (`minimal.jsonl`, `agent=100_txs=10000.jsonl`).

## Testing instructions
- Find the CI plan in `.github/workflows/` (`go.yml` for build/test/lint,
  `commitlint.yml` for commit messages).
- Run the full suite with `go test -gcflags=all='-N -l' ./...` (the `-N -l` disables
  inlining and matches `make test` / CI). One package: `go test ./agentsupervisor`;
  one test: add `-run TestXxx`; add `-cover` for coverage.
- CI pins `golangci-lint v2.7.0` and also enforces `go mod tidy -diff` — run
  `go mod tidy` before pushing or the pipeline fails.
- Lint with `golangci-lint run ./...` and auto-fix with `--fix` (matches `make
  lint-fix`). Fix all test, type, and lint errors until green; don't loop more than
  ~3 times on the same lint failure.
- After moving files or changing imports, re-run `go build ./...` and the linter so
  `gci` import ordering (standard → default → `prefix(github.com/HuangLab-SYSU/block-emulator-x)`
  → blank) stays correct.
- Add or update tests for code you change, even if nobody asked. Use `t.Run` subtests
  with `stretchr/testify` (`assert`/`require`); don't couple to implementation details.
- `agentsupervisor` has good unit-test coverage of trace compilation, registry, loop,
  and chainrunner — extend those when touching the agent layer.

## Coding conventions
- Naming: lowercase package/dir/file names, no dashes (except `_test.go`); prefer
  `nodeconfig` over `node_config`.
- Errors: never ignore; wrap with `fmt.Errorf("context: %w", err)` so `errors.Is` works.
- Logging: `log/slog` only (Debug/Info/Warn/Error). No `fmt.Print` / `log.Panic` in
  normal flow.
- Concurrency: the chain processes messages serially (single goroutine) to avoid
  races. `Chain` public methods must be called under its `mux` lock; internal methods
  assume the lock is held.
- On-wire messages are protobuf `WrappedMsg{MsgType, Payload}`; `message.WrapMsg`
  gob-encodes the payload. Send by `nodetopo.NodeInfo`, never raw IP.

## PR instructions
- Branch from `main` (no `develop` branch on `HuangLab-SYSU/agent-emulator`).
- Use Conventional Commits: `type(scope?): subject` (lowercase, imperative, <50
  chars). Types: feat, fix, docs, style, perf, refactor, ci, test, chore.
- Always run `go build ./...`, `go test -gcflags=all='-N -l' ./...`, `go mod tidy`,
  and `golangci-lint run ./... --fix` before committing.
- Validate a full run: `bash run_agentemu.sh` on the default trace — all txs
  committed, no Error/Warn logs, per-agent CSVs and figures produced.
