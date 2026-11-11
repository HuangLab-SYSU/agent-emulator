#!/usr/bin/env python3
"""Generate the gold-first, silver-fallback trace with 22,000 background pays."""

import argparse
import json
from pathlib import Path

from gen_treasury_trace import generate as generate_treasury


BUYER = "agent-metal-buyer"
GOLD = "agent-gold-supplier"
SILVER = "agent-silver-supplier"
FUNDERS = ("agent-funder-a", "agent-funder-b")
GOLD_OFFLINE_AT = 260000
GOLD_ONLINE_AT = 410000
FUNDING = [(40000, 35), (95000, 65), (120000, 55),
           (170000, 160), (270000, 100), (320000, 100),
           (420000, 160), (470000, 160)]
DEFAULT_OUT = Path(__file__).resolve().parents[1] / "traces" / "metals_savings_22000.jsonl"


def generate():
    treasury, _ = generate_treasury()
    # Preserve the unrelated workload and its lifecycle events.
    records = [
        row for row in treasury
        if row["agent_id"].startswith("agent-bg-")
        and (row["action"] != "pay" or row["target"].startswith("agent-bg-"))
    ]
    records.extend(
        {"agent_id": agent, "action": "join", "ts": 0, "request_id": f"metals-join-{agent}"}
        for agent in (BUYER, GOLD, SILVER, *FUNDERS)
    )
    records.extend([
        {"agent_id": GOLD, "action": "leave", "ts": GOLD_OFFLINE_AT, "request_id": "metals-gold-offline"},
        {"agent_id": GOLD, "action": "join", "ts": GOLD_ONLINE_AT, "request_id": "metals-gold-online"},
    ])
    records.extend(
        {"agent_id": FUNDERS[i % len(FUNDERS)], "action": "pay", "target": BUYER,
         "amount": amount, "ts": ts, "request_id": f"metals-funding-{i + 1:02d}"}
        for i, (ts, amount) in enumerate(FUNDING)
    )
    return sorted(records, key=lambda row: row["ts"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    args = parser.parse_args()
    records = generate()
    with args.out.open("w", encoding="utf-8", newline="\n") as out:
        for row in records:
            out.write(json.dumps(row, separators=(",", ":")) + "\n")
    print(f"Wrote {len(records):,} trace actions to {args.out}")


if __name__ == "__main__":
    main()
