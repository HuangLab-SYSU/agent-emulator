#!/usr/bin/env python3
"""Generate a feasibility-payment trace for AgentEmulator.

The trace is guaranteed to satisfy the Host's state machine: every pay's
sender and target are active (joined, not left) at that point, every leave
hits an active agent, and rejoins only happen after a leave. All randomness
derives from --seed, so the same invocation always yields the same file.

Example:
    python3 scripts/gen_pay_trace.py --agents 100 --pays 10000 \
        --seed 20260912 --out traces/pay_10k.jsonl
"""

import argparse
import json
import random


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--agents", type=int, default=100, help="number of agents")
    p.add_argument("--pays", type=int, default=10000, help="number of pay records")
    p.add_argument("--seed", type=int, default=20260912, help="random seed")
    p.add_argument("--out", default="traces/pay_10k.jsonl", help="output trace file")
    p.add_argument(
        "--lifecycle-every",
        type=int,
        default=250,
        help="one rejoin+leave rotation every N pays (keeps >= 10 agents active)",
    )
    args = p.parse_args()

    if args.agents < 2:
        p.error("--agents must be >= 2")

    rnd = random.Random(args.seed)
    records = []
    ts = 0

    def emit(rec):
        records.append(rec)

    # Phase 1: everyone joins.
    agents = [f"agent-{i:03d}" for i in range(args.agents)]
    for aid in agents:
        ts += 1
        emit({"agent_id": aid, "action": "join",
              "params_hash": f"doc-{aid}-v1", "ts": ts})

    active = set(agents)
    left = []  # agents currently left, in leave order

    # Phase 2: the payment flow, with periodic rejoin/leave rotations.
    for n in range(1, args.pays + 1):
        if args.lifecycle_every > 0 and n > 1 and (n - 1) % args.lifecycle_every == 0:
            # Rejoin the longest-left agent first, then leave a random active
            # one, so the active pool never drains.
            if left:
                aid = left.pop(0)
                ts += 1
                emit({"agent_id": aid, "action": "join",
                      "params_hash": f"doc-{aid}-rejoin", "ts": ts})
                active.add(aid)

            if len(active) > 10:
                pool = sorted(active)
                aid = pool[rnd.randrange(len(pool))]
                ts += 1
                emit({"agent_id": aid, "action": "leave",
                      "params_hash": f"exit-{aid}", "ts": ts})
                active.remove(aid)
                left.append(aid)

        pool = sorted(active)
        sender, target = rnd.sample(pool, 2)
        ts += 1
        emit({"agent_id": sender, "action": "pay", "target": target,
              "amount": rnd.randint(1, 100), "ts": ts,
              "request_id": f"pay-{n:05d}"})

    with open(args.out, "w") as f:
        for rec in records:
            f.write(json.dumps(rec, separators=(",", ":")) + "\n")

    n_pay = sum(1 for r in records if r["action"] == "pay")
    n_join = sum(1 for r in records if r["action"] == "join")
    n_leave = sum(1 for r in records if r["action"] == "leave")
    print(f"wrote {args.out}: {len(records)} records "
          f"({n_join} joins, {n_pay} pays, {n_leave} leaves), "
          f"{len(active)} active / {len(left)} left at end, ts=1..{ts}")


if __name__ == "__main__":
    main()
