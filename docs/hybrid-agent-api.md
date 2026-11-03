# Single API agent with trace-driven peers

Edit the existing `agent_api` section in `agentEmuConfig.yaml`: set
`enabled: true`, select an `agent_id` present in `experiment.trace`, and configure
`base_url` and `model` for a chat-completions-compatible provider.

The current configuration reads `api_key_file: ./.secrets/deepseek_api_key`.
Paste only the API key into that local plain-text file, then save it. Leading
and trailing whitespace (including the final newline) is trimmed. The file is
ignored by Git and initially created empty with owner-only permissions. Relative
paths are resolved against the YAML file's directory. Each decision rereads the
file, so replacing a key does not require changing configuration or exporting an
environment variable. Empty, missing, oversized or multiline keys produce a
clear error without displaying the key.

Alternatively, remove `api_key_file` and set `api_key_env` to an environment
variable name. Setting both is an error. Leaving both unset allows local
unauthenticated servers. The URL is the API prefix; the adapter appends
`/chat/completions`.

From the Go module root, after saving the key file:

```sh
go run ./cmd/agentemu -config agentEmuConfig.yaml
```

Use a fresh `base.result_dir` for every run. Existing registries or action
outputs are refused rather than reused. The ordinary `run_agentemu.sh` script
clears `exp/`, so invoke the command directly when preserving earlier results.
A valid provider key is required for successful decisions; no provider key is
shipped.

## Execution

`agent_api.enabled: false` (the default) retains the existing trace-only runner.
Online mode supports `static_relay` only and starts exactly one cluster. It does
not use `loop.max_rounds`: use `run_duration_seconds` to bound the experiment.

The selected agent keeps its first trace `join`. Its subsequent trace actions
(including `leave` and repeated `join`) are replaced by model decisions. Other
agents and plain background transfers retain their original trace order. A raw
transfer spending the selected agent's derived account is refused. A selected
agent without a trace join is a configuration error. Counts of replaced actions
are logged and recorded.

Online mode interprets differences in trace `ts` as wall-clock milliseconds,
relative to the first retained record. `trace_time_scale: 1` replays real time;
`2` replays twice as fast. This affects only online mode. Injection is bounded by
`supervisor.tx_injection_speed` each second; delayed records keep their order and
catch up after snapshot barriers. Trace actions beyond the experiment duration
are not injected and are counted as remaining in the summary. Plain transfers
inherit the preceding trace timestamp as in legacy mode.

The API identity starts deciding after its registration transactions have
settled. Decisions run independently of trace injection, at most one request at
a time. Its next decision also waits for its preceding submitted transaction to
settle. The configured interval starts after validation/failure of the preceding
decision. A model response is exactly one JSON object:

```json
{"action":"pay","target":"agent-bob","amount":"5"}
```

or `{"action":"wait"}` / `{"action":"leave"}`. The model never chooses the
sender, DID, nonce, transaction hash, or request ID. Payment amounts are positive
decimal strings in the existing uint64 range. Self payments, inactive recipients,
unknown fields, malformed JSON, and trailing output are rejected. In online mode,
background actions that violate mutable lifecycle rules (inactive payer/payee,
repeated leave), zero-value agent payments, and unsupported actions are rejected
before compilation. They are appended immediately to
`round_001/trace_failures.jsonl` with the original trace line, scheduled action,
request ID, failure time and reason. The cursor advances and subsequent trace
records and API decisions continue. Rejected actions do not mutate the registry,
consume nonces, generate transactions or appear in successful action mappings.
`trace_actions_failed` in `hybrid_summary.json` counts these rejections.

Malformed input files, unauthorized raw spending of the API wallet, compiler,
file-write, snapshot and chain errors remain fatal. This policy handles
pre-submission action rejections; it does not introduce on-chain failure receipts
or a general recovery mechanism for failed consensus/execution. Legacy
trace-only mode keeps its existing strict validation behavior.

The model sees registered agents' active status, confirmed balances, spendable
balances after pending debit reservations, per-shard block heights, pending
transaction count, its previous decision result, and each shard's latest block
hash, timestamp, and up to 32 transaction summaries (with a truncation flag). Snapshots come from the
leaders' actual state databases, not reconstructed CSV balances. The supervisor
briefly withholds proposals and drains existing permits before taking a stable
snapshot, then resumes under the decision quota. It repeats the barrier and
reads fresh balances before accepting a returned payment. Cross-shard transfers
are pending until both relay stages have committed; the snapshot explicitly
reports pending transactions and may include partially settled transfers.

HTTP transport errors, status 429, and server errors are retried at most twice,
within a single `decision_timeout_seconds` deadline. Authentication errors,
invalid responses, and invalid actions are recorded as failed decisions and
skipped; they do not abort the experiment. A failed/expired request releases its
block restriction. Provider protocols can be added by implementing `DecisionModel`
and selecting them in the model constructor; currently the supported adapter is
`chat-completions`.

## Large treasury scenario

The current configuration selects a 22,156-record treasury experiment with
22,000 valid ordinary payments, staged funding, provider failover and surplus
refunds. See [the scenario guide](scenarios/treasury_stress_22000.md). Model
observations include `elapsed_ms` and `settled_payments_by_target` (this API
identity's confirmed cumulative outgoing payments only) to support deadlines and
prevent paying completed obligations again. These totals persist across wait
decisions within one run; crash recovery is still not supported.

## Waiting for funds scenario

The smaller funding example can be selected with
`experiment.trace: ./traces/wait_for_funds.jsonl`, `agent_api.agent_id: agent-alice`,
`agent_api.initial_balance: "2"`, and the goal described below. It gives
Alice an initial balance of 2. Her goal is to pay Bob exactly 10 once,
waiting while `available_balance < 10`. Carol submits a transfer of 3 at trace
+35 seconds and another of 5 at +70 seconds. Submission times are relative to
runtime start; the funds become spendable only after their chain settlement.
Expected observations are 2 → 5 → 10 → 0, with `wait` decisions before the
threshold and one `pay` of 10 once enough funds have settled. Actual model
choices remain model-generated and are recorded/validated, not scripted.

The allocation is written into the owning shard's genesis state. Explicitly
configured accounts stay at zero after spending their funds; the legacy lazy
account funding must not replenish them. Live snapshots, transfer checks, and
CSV ledgers all use the same allocation. Omitting `initial_balance` keeps the
original 10^36 default. Non-negative decimal strings up to uint256 are accepted,
including explicit zero; transfer amounts still use the existing uint64 range.

The thinking log now includes `balance` and `available_balance` on each start
line. Inspect these alongside `action=wait`, followed by `action=pay amount=10`
and its confirmation hash. Results go to `exp/wait-for-funds` so the earlier
DeepSeek experiment is preserved. Choose another fresh result directory for
subsequent runs. The optional funding integration test uses a deterministic
local mock model to validate the same behavior without a paid API call:

```sh
AGENTEMU_INTEGRATION=1 go test ./agentsupervisor \
  -run TestHybridWaitForFundsIntegration -count=1 -timeout=4m -v
```

## Global block budget

`max_blocks_during_decision` limits the **total blocks across all shards** after
the observation barrier and while a model call is running. The default is 5;
explicit `0` allows no new blocks during the call. Empty blocks and blocks
containing background trace payments both count. Genesis is before observation
and does not consume a decision budget.

Every leader requests a permit before proposing. The supervisor reserves the
quota before granting permission, including proposals still in consensus, so
simultaneous shards cannot overshoot the cap. A round-robin allocator distributes
waiting requests. Exhausting the quota withholds new proposals while nodes keep
processing messages and the model keeps running. The controller, never the
model, owns suspension and resumption. A slow API therefore also delays other
agents' on-chain confirmations; this is the intended experimental policy.

At the end of the active duration the supervisor cancels an ongoing model call,
stops reading further trace actions, releases the budget, and drains submitted
transactions for up to `drain_timeout_seconds`. It then takes a final snapshot
and shuts down the existing cluster. The outer `chain.run_timeout_seconds` must
cover startup, active duration and drain. Ctrl-C tears down child processes.
Model calls use wall-clock time; this is not a deterministic virtual-time
simulation. Captured actions can be replayed without recalling the model, but
model outputs and concurrent block timing are not reproducible from a seed alone.

## Results and validation

`agent_thinking.log` is written live in `base.result_dir` and mirrored to the
running terminal, independently of the chain log level. Follow it in another
terminal with `tail -f exp/treasury-stress-22000/agent_thinking.log`.

Each model call writes `thinking_started` (agent, model, decision number,
observation time/heights and block quota), then `thinking_finished` (elapsed time,
returned action/target/amount or API failure). Both completion and result lines
include a `summary`: a brief Chinese decision explanation supplied by the model,
for example `当前可用余额充足，决定向 agent-bob 转账 5。`. If the model
omits it, the runtime describes the action instead. Summaries are flattened to
one line and capped at 120 characters; detailed model reasoning is not printed. A `decision_result` line records
subsequent validation as accepted/rejected/failed, including the transaction hash
when a transaction was submitted. An accepted decision is not yet proof of final
chain settlement; confirm the hash in the ledger or chain measurements. Wait
and leave decisions are logged too. Timeout, cancellation and experiment end
are recorded without printing credentials or raw model reasoning. Start/end
timestamps follow the runtime's existing audit records; completion is logged on
the next supervisor tick after the API response (normally within one second).


- `hybrid_summary.json`: model identifier, replaced/remaining trace action
  counts, pending count, final confirmed snapshot, per-decision observation,
  raw provider response (including usage when supplied), result, model elapsed
  time, and blocks granted/committed during the call. Treat this file as model
  output that may contain experiment data; credentials are never recorded.
- `hybrid_config.yaml`: normalized run configuration, with the environment
  variable name or the resolved key-file path instead of the key value.
- `round_001/input_actions.jsonl`: captured accepted trace/model actions.
- `round_001/trace_failures.jsonl`: rejected background actions and reasons;
  these were not submitted on chain. The summary counts `trace_actions_failed`.
- `round_001/agent_transactions.jsonl`, `agent_action_txs.jsonl`,
  `Agent_Events.csv`, `agents/*.csv`: existing plan, mapping, event and ledger
  formats for the whole persistent run. Nonces are shared throughout the run.
- `round_001/chain/`: one cluster's databases, logs and measurements.
- `rounds_summary.json`: one continuous chain execution, not model-call rounds.

The existing offline CSV exporter runs only after databases are closed. API
latency is recorded separately from the chain's transaction creation-to-commit
measurements. The per-block permit exchange adds coordination overhead, so this
mode is intended to evaluate the configured mixed-agent policy rather than to
measure unmodified BlockEmulator-X throughput.

Run unit tests with `go test ./agentsupervisor`. The optional real-cluster test
launches two shards with four PBFT nodes each and a local fake API. It exercises
a delayed successful payment, a timeout, cross-shard settlement, persistent
balances, and the global budget without an external provider:

```sh
AGENTEMU_INTEGRATION=1 go test ./agentsupervisor \
  -run TestHybridPersistentChainIntegration -count=1 -timeout=4m -v
```

The integration test binds the normal local cluster ports; run it without
another experiment using those ports. Crash recovery and simultaneous multiple
API identities are outside this initial mode.
