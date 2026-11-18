# Treasury stress scenario: 22,000 background payments

This mixed-agent scenario uses the existing `join` / `pay` / `leave` actions.
Service obligations are represented by payment events; no compute delivery or
RWA matching engine is involved. One API-controlled treasury agent acts among
110 trace-driven peers on the persistent chain.

## Dataset

| Item | Count |
|---|---:|
| Agents, including the API identity | 111 |
| Ordinary background payments | 22,000 |
| Staged funding transfers to the API identity | 7 |
| Valid join/rejoin records | 117 |
| Valid leave records | 8 |
| Deliberately invalid payment probes | 24 |
| Total input records | 22,156 |
| Expected valid trace transactions, before model actions | 22,132 |

The 22,000 ordinary payments are lifecycle-valid and independent of the API
agent's choices. Invalid probes are additional records and are not counted toward
the 20,000-payment requirement. Actual completed on-chain volume still depends
on running the experiment to completion and successfully draining the chain.

Files:

- `traces/treasury_stress_22000.jsonl`: generated trace.
- `traces/treasury_stress_22000.manifest.json`: seed, SHA-256, counts, deadlines,
  funding schedule, expected rejected input lines and the recommended goal.
- `scripts/gen_treasury_trace.py`: deterministic generator and lifecycle validator.
- `scripts/test_gen_treasury_trace.py`: offline dataset checks and feasibility oracle.

## Agent requirements

`agent-treasurer` starts with a real balance of 15. Keep at least 20 after each
payment, perform one action per decision, and complete tasks in strict priority
order. Payment history comes from `settled_payments_by_target`, which includes
only this API agent's outgoing payments after final chain confirmation.

| Priority / obligation | Preferred provider | Backup provider | Price | Target deadline |
|---|---|---|---|---|
| 1. Audit | `agent-audit-main` | `agent-audit-backup` | 80 either | 160 s |
| 2. Storage | `agent-storage-main` | `agent-storage-backup` | 120 either | 300 s |
| 3. Compute | `agent-compute-main` | `agent-compute-backup` | 160 main / 180 backup | 420 s |

Do not pay a lower-priority obligation before the higher one is confirmed. Do
not pay a completed task again. Prefer an active main, otherwise choose an active
backup. If the active provider's price plus the reserve exceeds available funds,
wait. An overdue obligation is still owed; deadlines are evaluation targets,
not permission to abandon the task. After all tasks are confirmed, refund all
surplus above 20 to `agent-finance`. Refund future funding too, and remain active.

The platform supplies `elapsed_ms` so deadline comparisons use experiment time,
not a guessed wall-clock start. Confirmed payment totals remain available after
many wait decisions; a provider rejoining must not cause a completed task to be
paid again. The prompt and ordinary validation drive model behavior. The runtime
does not hardcode provider selection, task ordering or the reference policy.

## Scheduled disturbances and funding

Times below are relative to online runtime start. A transfer submitted at that
time becomes spendable only after its chain settlement.

| Time | Trace event | Expected treasury behavior with timely decisions |
|---|---|---|
| 0 s | Everyone joins; treasury balance 15 | Wait |
| 40 s | Funder A sends 40 | Balance 55: wait for audit funding |
| 70 s | Main audit provider leaves | Use audit backup when affordable |
| 100 s | Funder B sends 60 | Balance 115: pay audit backup 80, retain 35 |
| 150 s | Main storage provider leaves | Storage backup becomes the available choice |
| 170 s | Funder C sends 65 | Balance 100: wait; need 140 including reserve |
| 230 s | Funder A sends 80 | Balance 180: pay storage backup 120, retain 60 |
| 260 s | Main compute provider leaves | Compute backup costs 180 |
| 300 s | Funder B sends 90 | Balance 150: wait; need 200 including reserve |
| 350 s | Funder C sends 100 | Balance 250: pay compute backup 180, retain 70 |
| After confirmation | All obligations completed | Refund surplus 50, retain 20 |
| 440 s | Funder A sends a late 150 | Refund another 150, retain 20 |
| 450 s | Main compute provider rejoins | Do not repeat the completed compute payment |
| Before 480 s | Last ordinary background payment is scheduled | Background flow finishes |
| 600 s | Active runtime ends | Drain submitted transactions and export results |

On-time reference accounting is: initial 15 + funding 585 = provider payments
380 + refunds 200 + final reserve 20. If compute is paid late after its main
rejoins, the cheaper main price changes provider spending/refund totals; evaluate
the observed route and deadline rather than treating the reference as guaranteed.

There are 100 background agents and five temporary leave/rejoin rotations.
Ordinary payments avoid inactive peers. Background volume is split into 6,000,
10,000 and 6,000 payments over roughly 0–160, 160–320 and 320–480 seconds, with
mixed small/medium/large values. After each main provider leaves, eight extra
payments deliberately target it while inactive: 24 expected failures in total.
These should be recorded and skipped while later transactions continue.

## Configuration and execution

The existing `agentEmuConfig.yaml` is already configured for this scenario:

- `experiment.trace: ./traces/treasury_stress_22000.jsonl`
- `base.result_dir: ./exp/treasury-stress-22000`
- `agent_api.agent_id: agent-treasurer`, `initial_balance: "15"`
- The full task policy is in `agent_api.goal`; the model and key-file settings are retained.
- Runtime 600 s, model timeout 60 s, decision interval 10 s, drain timeout 120 s.
- Global decision-time block budget remains 5; outer chain timeout is 900 s.
- The separate `config.yaml` remains as edited by the user (currently 4 shards,
  4 nodes each and a 2,000-transaction block limit).

After placing the key in its configured local file, run from the module root:

```sh
go run ./cmd/agentemu -config agentEmuConfig.yaml
```

Use a fresh result directory for each subsequent run. To observe the agent:

```sh
tail -F exp/treasury-stress-22000/agent_thinking.log
```

Regenerate and check the dataset without any model calls:

```sh
python3 scripts/gen_treasury_trace.py
python3 scripts/test_gen_treasury_trace.py
```

## What to check

- `hybrid_summary.json`: `phase=finished`, no pending transactions, no remaining
  trace records, and 24 failed trace actions if all probes were reached.
- `round_001/trace_failures.jsonl`: the manifest's 24 inactive-provider probes;
  unexpected additional failures should be investigated.
- The action-to-transaction map and chain measurements: all ordinary background
  payments confirmed, plus valid funding/lifecycle transactions and API payments.
- `settled_payments_by_target`: no repeated task payment, appropriate main/backup
  choice, correct confirmed refund totals.
- `agent_thinking.log`: waits when funds are insufficient, reserve-aware payment
  amounts, route changes, elapsed time and accumulated confirmed payments.
- Agent CSVs and final snapshots: treasury retains 20 after all refunds, and
  background and API transfers conserve balances.

Real model outputs, chain timing and provider latency can vary. The offline
oracle tests feasibility using the intended policy; they do not prove that a
particular live model will follow it. High background volume and block-budget
pauses may add delay. If records remain at shutdown, increase runtime or slow the
trace rather than counting unsubmitted records as completed transactions.
