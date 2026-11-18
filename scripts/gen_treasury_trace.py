#!/usr/bin/env python3
"""Generate a deterministic mixed-agent treasury scenario with >=20k valid pays.

No model calls or chain processes are started. Scheduled invalid probes exercise
online trace rejection; all ordinary background payments remain lifecycle-valid.
"""

import argparse
import collections
import hashlib
import json
from pathlib import Path
import random


SEED = 20260903
TREASURER = "agent-treasurer"
BACKGROUND = [f"agent-bg-{i:03d}" for i in range(1, 101)]
PROVIDERS = ["agent-audit-main", "agent-audit-backup", "agent-storage-main",
             "agent-storage-backup", "agent-compute-main", "agent-compute-backup"]
FUNDERS = ["agent-funder-a", "agent-funder-b", "agent-funder-c"]
FINANCE = "agent-finance"
AGENTS = [TREASURER, *PROVIDERS, *FUNDERS, FINANCE, *BACKGROUND]
ROTATIONS = [("agent-bg-005", 50000, 90000),
             ("agent-bg-017", 130000, 180000),
             ("agent-bg-042", 210000, 260000),
             ("agent-bg-063", 290000, 340000),
             ("agent-bg-084", 370000, 420000)]
FUNDING = [(40000, 40), (100000, 60), (170000, 65), (230000, 80),
           (300000, 90), (350000, 100), (440000, 150)]
TASKS = [
    {"task": "audit", "priority": 1, "deadline_ms": 160000,
     "main": "agent-audit-main", "main_price": 80,
     "backup": "agent-audit-backup", "backup_price": 80},
    {"task": "storage", "priority": 2, "deadline_ms": 300000,
     "main": "agent-storage-main", "main_price": 120,
     "backup": "agent-storage-backup", "backup_price": 120},
    {"task": "compute", "priority": 3, "deadline_ms": 420000,
     "main": "agent-compute-main", "main_price": 160,
     "backup": "agent-compute-backup", "backup_price": 180},
]
GOAL = """Act as a treasury payment agent. The user message contains elapsed_ms,
available_balance, active counterparties, and settled_payments_by_target (only
YOUR confirmed outgoing API payments; an absent target means zero paid).
Keep a reserve of at least 20 after every payment. Never leave or pay yourself.
Complete these obligations exactly once in strict priority order:
1. Audit: pay exactly 80 to agent-audit-main, or 80 to agent-audit-backup if the
main is inactive. Target completion before elapsed_ms=160000.
2. Storage, only after audit is confirmed: pay exactly 120 to agent-storage-main,
or 120 to agent-storage-backup if the main is inactive. Target completion before
elapsed_ms=300000.
3. Compute, only after storage is confirmed: pay exactly 160 to agent-compute-main,
or 180 to agent-compute-backup if the main is inactive. Target completion before
elapsed_ms=420000.
A task is completed when settled_payments_by_target shows its full price paid
to either listed provider. Do not pay that task again, even if its main later
rejoins. For an incomplete task prefer its active main; otherwise use its active
backup at the stated price. If neither is active, or available_balance is less
than the price plus the 20 reserve, return wait. Do not skip an unpaid higher
priority task or make partial payments. Use an affordable active backup rather
than waiting for an offline main to return. Deadlines are evaluation targets;
overdue unpaid tasks still need completion.
Once all three are confirmed, return all available_balance above 20 to
agent-finance if it is active. Repeat surplus refunds if later funding arrives;
otherwise wait. Refunds to finance do not count as provider payments.
Choose only one action per decision: pay, wait, or leave (leaving is prohibited
by this goal). Amounts must be decimal integer strings. Never assume a submitted
or rejected payment is confirmed; use settled_payments_by_target."""


def generate(seed=SEED, background_pays=22000):
    if background_pays < 20000:
        raise ValueError("background_pays must be >= 20000")
    rnd = random.Random(seed)
    events = []

    def emit(ts, priority, record):
        events.append((ts, priority, len(events), dict(record, ts=ts)))

    for i, aid in enumerate(AGENTS, 1):
        emit(0, 0, {"agent_id": aid, "action": "join",
                    "request_id": f"initial-join-{i:03d}", "params_hash": f"{aid}-v1"})
    retirement = [("agent-audit-main", 70000), ("agent-storage-main", 150000),
                  ("agent-compute-main", 260000)]
    for aid, ts in retirement:
        emit(ts, 1, {"agent_id": aid, "action": "leave",
                     "request_id": f"retire-{aid}"})
        for i in range(1, 9):
            emit(ts + i * 5000, 2, {"agent_id": BACKGROUND[0], "action": "pay",
                 "target": aid, "amount": i, "request_id": f"probe-inactive-{aid}-{i:02d}"})
    emit(450000, 1, {"agent_id": "agent-compute-main", "action": "join",
                    "request_id": "compute-main-returns", "params_hash": "compute-main-v2"})
    for aid, leave_at, rejoin_at in ROTATIONS:
        emit(leave_at, 1, {"agent_id": aid, "action": "leave",
                          "request_id": f"rotate-leave-{aid}"})
        emit(rejoin_at, 1, {"agent_id": aid, "action": "join",
                           "request_id": f"rotate-join-{aid}", "params_hash": f"{aid}-v2"})
    for i, (ts, amount) in enumerate(FUNDING, 1):
        emit(ts, 2, {"agent_id": FUNDERS[(i-1) % len(FUNDERS)], "action": "pay",
                     "target": TREASURER, "amount": amount,
                     "request_id": f"funding-{i:02d}"})

    # Three load phases, with the largest share in the middle 160-second window.
    counts = [background_pays * 3 // 11, background_pays * 5 // 11]
    counts.append(background_pays - sum(counts))
    phases = [(5000, 160000), (160000, 320000), (320000, 480000)]
    payment = 0
    for (start, end), count in zip(phases, counts):
        for i in range(count):
            ts = start + ((end-start) * i // count)
            active = [aid for aid in BACKGROUND if not any(
                aid == changed and leave_at <= ts < rejoin_at
                for changed, leave_at, rejoin_at in ROTATIONS)]
            sender, target = rnd.sample(active, 2)
            tier = rnd.random()
            amount = rnd.randint(1, 50) if tier < .7 else (
                rnd.randint(51, 500) if tier < .95 else rnd.randint(501, 2000))
            payment += 1
            emit(ts, 3, {"agent_id": sender, "action": "pay", "target": target,
                         "amount": amount, "request_id": f"background-{payment:06d}"})
    records = [record for _, _, _, record in sorted(events)]
    report = validate(records)
    return records, report


def validate(records):
    active = set()
    seen_ids = set()
    counts = collections.Counter()
    failures = []
    last_ts = -1
    for line, record in enumerate(records, 1):
        if record["ts"] < last_ts:
            raise ValueError(f"unsorted timestamp at line {line}")
        last_ts = record["ts"]
        request_id = record["request_id"]
        if request_id in seen_ids:
            raise ValueError(f"duplicate request_id: {request_id}")
        seen_ids.add(request_id)
        aid, action = record["agent_id"], record["action"]
        invalid = False
        if action == "join":
            if aid in active:
                raise ValueError(f"redundant join at line {line}")
            active.add(aid)
        elif action == "leave":
            if aid not in active:
                raise ValueError(f"invalid leave at line {line}")
            active.remove(aid)
        elif action == "pay":
            if not isinstance(record["amount"], int) or record["amount"] <= 0:
                raise ValueError(f"invalid amount at line {line}")
            invalid = aid not in active or record["target"] not in active
            if invalid != request_id.startswith("probe-inactive-"):
                raise ValueError(f"unexpected lifecycle validity at line {line}")
            if aid == TREASURER:
                raise ValueError("API identity must not have scripted payments")
        else:
            raise ValueError(f"unsupported action at line {line}")
        if invalid:
            counts["expected_failed_payments"] += 1
            failures.append({"trace_line": line, "request_id": request_id,
                             "target": record["target"], "scheduled_ms": record["ts"]})
        else:
            counts[f"valid_{action}"] += 1
            if request_id.startswith("background-"):
                counts["valid_background_payments"] += 1
    if counts["valid_background_payments"] < 20000 or len(failures) != 24:
        raise ValueError("scenario must contain >=20k valid background pays and 24 rejection probes")
    return {"total_records": len(records), "agent_count": len(AGENTS),
            **dict(counts), "expected_valid_trace_transactions": sum(
                counts[f"valid_{action}"] for action in ("join", "leave", "pay")),
            "last_scheduled_ms": last_ts, "expected_failures": failures}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=int, default=SEED)
    parser.add_argument("--background-pays", type=int, default=22000)
    parser.add_argument("--out", type=Path, default=Path("traces/treasury_stress_22000.jsonl"))
    args = parser.parse_args()
    records, report = generate(args.seed, args.background_pays)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    payload = "".join(json.dumps(record, separators=(",", ":")) + "\n" for record in records).encode()
    args.out.write_bytes(payload)
    report.update({"seed": args.seed, "sha256": hashlib.sha256(payload).hexdigest(),
                   "api_agent_id": TREASURER, "initial_balance": "15", "reserve": 20,
                   "run_duration_seconds": 600, "tasks": TASKS,
                   "funding": [{"at_ms": ts, "amount": amount} for ts, amount in FUNDING],
                   "total_funding": sum(amount for _, amount in FUNDING),
                   "expected_on_time_provider_spending": 380,
                   "expected_on_time_finance_refund": 200,
                   "expected_final_balance": 20,
                   "recommended_goal": GOAL})
    manifest = args.out.with_suffix(".manifest.json")
    manifest.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({key: value for key, value in report.items()
                      if key not in ("recommended_goal", "expected_failures")}, indent=2))
    print(f"Trace: {args.out}\nManifest: {manifest}")


if __name__ == "__main__":
    main()
