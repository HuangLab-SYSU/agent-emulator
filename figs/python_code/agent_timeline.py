"""Render the live Agent's timeline from the artifacts of a hybrid run."""

import csv
import json
import re
from bisect import bisect_right
from datetime import datetime
from html import escape
from pathlib import Path

COMMIT_LOG = re.compile(r'^time=(\S+) .*?msg="block is added".*?"block height"=(\d+)')
COLORS = ("#087e8b", "#7350a1", "#bb6400", "#227447")


def localized(zh, en, tag="span"):
    return f'<{tag} data-zh="{escape(zh)}" data-en="{escape(en)}">{escape(zh)}</{tag}>'


def parse_time(value):
    # Go emits RFC3339 with variable-width fractions. Python 3.9 accepts
    # exactly three or six fractional digits.
    value = re.sub(r"\.(\d+)(?=Z|[+-]\d{2}:\d{2}$)", lambda m: "." + m[1][:6].ljust(6, "0"), value)
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def epoch_ms(value):
    return round(parse_time(value).timestamp() * 1000) if value else None


def read_commits(summary, logs):
    """Key blocks by (shard, height), counting each leader's commit once."""
    rows = summary.get("block_commits")
    if rows is not None:
        return {(row["shard"], row["height"]): epoch_ms(row["committed_at"]) for row in rows}

    # Older runs only recorded block completion in the leader logs.
    commits = {}
    for path in logs.glob("node_s*_n0.log"):
        shard = int(path.stem.split("_")[1][1:])
        with path.open(encoding="utf-8") as source:
            for line in source:
                match = COMMIT_LOG.search(line)
                if match:
                    time, height = match.groups()
                    commits.setdefault((shard, int(height)), epoch_ms(time))
    return commits


def read_timeline(result_dir, agents_dir):
    summary_path = result_dir / "hybrid_summary.json"
    if not summary_path.is_file():
        return None

    summary = json.loads(summary_path.read_text(encoding="utf-8"))
    agent = summary["agent_id"]
    round_dir = agents_dir.parent
    commits = read_commits(summary, round_dir / "chain" / "logs")
    complete = len(commits) == summary["blocks_committed"]
    commit_times = sorted(commits.values()) if complete else None
    registry = json.loads((result_dir / "agent_registry.json").read_text(encoding="utf-8"))
    address = next((entry["did"] for entry in registry if entry["agent_id"] == agent), None)
    # The hybrid runtime uses static relay, with the account location fixed
    # by the last four address bytes modulo the configured shard count.
    shard_count = len(summary["latest_snapshot"]["shard_heights"])
    shard = int(address[-8:], 16) % shard_count if address and complete and shard_count else None

    ledger_path = agents_dir / (re.sub(r"[^a-zA-Z0-9._-]", "_", agent) + ".csv")
    balances = []
    if ledger_path.is_file():
        with ledger_path.open(encoding="utf-8", newline="") as source:
            for row in csv.DictReader(source):
                committed = commits.get((shard, int(row["block_height"])))
                if committed is None:
                    # Legacy ledger timestamps come from the block header (proposal time).
                    commit_times = None
                    committed = int(row["block_time_ms"])
                balances.append((committed, int(row["balance"])))
    balances.sort(key=lambda row: row[0])

    audits = summary.get("decisions") or []
    hashes = {audit["transaction_hash"] for audit in audits if audit.get("transaction_hash")}
    injections = {}
    links = round_dir / "agent_action_txs.jsonl"
    if hashes and links.is_file():
        with links.open(encoding="utf-8") as source:
            for line in source:
                action = json.loads(line)
                for tx_hash in hashes.intersection(action["tx_hashes"] or []):
                    injections[tx_hash] = action["ts"]

    decisions = []
    for audit in audits:
        decision = audit["reply"]["decision"]
        started = epoch_ms(audit["started"])
        finished = epoch_ms(audit["finished"])
        action = decision["action"]
        result = audit["result"]
        has_reply = result in ("accepted", "experiment duration reached",
                               "runtime stopped before decision validation completed") or result.startswith("rejected:")
        proposed = finished if has_reply and action in ("pay", "leave") else None
        injected = injections.get(audit.get("transaction_hash"))
        decisions.append({
            "number": audit["observation"]["decision"],
            "events": [(epoch_ms(audit.get("snapshot_started")), started),
                       (started, finished), (proposed, proposed), (injected, injected)],
            "description": decision.get("summary") or " ".join(
                str(decision[key]) for key in ("action", "target", "amount") if decision.get(key)
            ),
            "result": result,
        })

    reference = summary["latest_snapshot"]["observed_at"]
    zone = parse_time(reference).tzinfo
    return agent, balances, decisions, commit_times, zone, epoch_ms(reference)


def absolute_time(value, zone):
    return datetime.fromtimestamp(value / 1000, zone).isoformat(sep=" ", timespec="milliseconds")


def render_chart(balances, decisions, commit_times, zone, end, by_block):
    width, height = 1100, 408
    left, right = 150, width - 30
    labels = ("区块快照 / Snapshot", "Agent 思考 / Thinking",
              "新 trace / Proposed trace", "trace 注入 / Injection")
    times = [time for time, _ in balances] + [
        time for decision in decisions for interval in decision["events"] for time in interval
        if time is not None
    ] + [end]
    start = 0 if by_block else min(times)
    stop = len(commit_times) if by_block else max(times)

    def x(value):
        coordinate = bisect_right(commit_times, value) if by_block else value
        return left + (right - left) * (coordinate - start) / max(stop - start, 1)

    def describe(time):
        blocks = "" if commit_times is None else f" · #{bisect_right(commit_times, time)}"
        return absolute_time(time, zone) + blocks

    svg = [f'<svg viewBox="0 0 {width} {height}" role="img" aria-label="Agent timeline">']
    for i in range(7):
        tick = start + (stop - start) * i // 6
        tx = left + (right - left) * (tick - start) / max(stop - start, 1)
        label = str(tick) if by_block else datetime.fromtimestamp(tick / 1000, zone).strftime("%H:%M:%S")
        svg.append(f'<path d="M{tx:.2f},22 V365" stroke="#e0e6ed"/>'
                   f'<text x="{tx:.2f}" y="385" text-anchor="middle">{label}</text>')

    svg.append(f'<text x="{left - 10}" y="20" text-anchor="end">余额 / Balance</text>')
    if balances:
        low = min(balance for _, balance in balances)
        high = max(balance for _, balance in balances)

        def y(balance):
            return 178 - 137 * (balance - low) / max(high - low, 1)

        for balance in sorted({low, high}):
            label = f"{balance:.3e}" if abs(balance) >= 10**12 else str(balance)
            svg.append(f'<text x="{left - 10}" y="{y(balance) + 4:.2f}" text-anchor="end">{label}</text>')
        first_time, first_balance = balances[0]
        steps = [f"M{x(first_time):.2f},{y(first_balance):.2f}"]
        for time, balance in balances[1:]:
            steps.append(f"H{x(time):.2f} V{y(balance):.2f}")
        steps.append(f"H{x(max(times)):.2f}")
        svg.append(f'<path d="{" ".join(steps)}" fill="none" stroke="#245ba2" stroke-width="2"/>')
        for time, balance in balances:
            svg.append(f'<circle cx="{x(time):.2f}" cy="{y(balance):.2f}" r="3.5" fill="#245ba2">'
                       f'<title>{describe(time)} · {balance}</title></circle>')

    for lane, (label, color) in enumerate(zip(labels, COLORS)):
        ty = 225 + lane * 40
        svg.append(f'<text x="{left - 10}" y="{ty + 4}" text-anchor="end">{label}</text>'
                   f'<path d="M{left},{ty} H{right}" stroke="#d9e0e8"/>')
        for decision in decisions:
            begin, finish = decision["events"][lane]
            if finish is None:
                continue
            duration = describe(finish)
            if begin is not None and begin != finish:
                duration = describe(begin) + " → " + duration
            title = escape(f'#{decision["number"]} · {duration} · {decision["description"]}')
            if begin is not None and lane < 2:
                svg.append(f'<line x1="{x(begin):.2f}" x2="{x(finish):.2f}" y1="{ty}" y2="{ty}" '
                           f'stroke="{color}" stroke-width="6"><title>{title}</title></line>')
            markers = (finish,) if begin is None or begin == finish else (begin, finish)
            for time in markers:
                fill = "#fff" if begin is None else color
                svg.append(f'<circle cx="{x(time):.2f}" cy="{ty}" r="4" fill="{fill}" stroke="{color}">'
                           f'<title>{title}</title></circle>')
    svg.append("</svg>")
    return "".join(svg)


def timeline_section(result_dir: Path, agents_dir: Path):
    data = read_timeline(result_dir, agents_dir)
    if data is None:
        return ""

    agent, balances, decisions, commit_times, zone, end = data
    chart_args = balances, decisions, commit_times, zone, end
    parts = ["""<section class="timeline">
<style>
.timeline figure { margin: 12px 0; padding: 12px; overflow-x: auto; background: #fff; border-radius: 8px; }
.timeline figure svg { display: block; min-width: 840px; width: 100%; font: 12px sans-serif; fill: #334155; }
.timeline input { margin-left: 16px; }
.timeline .block-axis { display: none; }
.timeline #timeline-block:checked ~ .time-axis { display: none; }
.timeline #timeline-block:checked ~ .block-axis { display: block; }
.timeline .timeline-table { overflow-x: auto; }
.timeline table { border-collapse: collapse; width: 100%; background: #fff; font-size: 13px; }
.timeline th, .timeline td { border-bottom: 1px solid #dbe3ed; padding: 8px; text-align: left; }
.timeline time { white-space: nowrap; }
</style>""", localized(f"{agent} 实验时间线", f"{agent} experiment timeline", "h2"),
        '<input id="timeline-time" type="radio" name="timeline-axis" checked>',
        '<label for="timeline-time">' + localized("绝对时间", "Absolute time") + "</label>",
    ]
    if commit_times is not None:
        parts += ['<input id="timeline-block" type="radio" name="timeline-axis">',
                  '<label for="timeline-block">' + localized("系统累计区块数", "Total committed blocks") + "</label>"]
    else:
        parts.append(localized("区块提交记录不完整，缺失的余额提交时间用区块提议时间代替。区块轴不可用。",
                               "Commit history is incomplete. Ledger block times fill missing commit times. The block axis is unavailable.", "p"))
    parts.append(f'<figure class="time-axis">{render_chart(*chart_args, by_block=False)}'
                 f'<figcaption>{absolute_time(end, zone)[:10]} · UTC{datetime.fromtimestamp(end / 1000, zone).strftime("%z")}</figcaption></figure>')
    if commit_times is not None:
        parts.append(f'<figure class="block-axis">{render_chart(*chart_args, by_block=True)}<figcaption>'
                     + localized("累计各分片已提交区块（含空块，排除创世块）。出块暂停时，多个事件可能落在同一位置。",
                                 "Committed blocks across all shards, including empty blocks and excluding genesis. Events can share a position while commits are paused.")
                     + "</figcaption></figure>")
    if any(decision["events"][0][0] is None for decision in decisions):
        parts.append(localized("旧实验缺少区块快照开始时间，空心点表示快照收集结束。",
                               "This run did not record snapshot start times. Hollow markers show snapshot completion.", "p"))

    def event_cell(value):
        if value is None:
            return "—"
        label = absolute_time(value, zone).split(" ", 1)[1]
        blocks = "" if commit_times is None else f" · #{bisect_right(commit_times, value)}"
        return f"<time>{label}{blocks}</time>"

    parts += ["<details><summary>", localized("逐轮查看时间点与决策", "Decision details"),
              '</summary><div class="timeline-table"><table><thead><tr>']
    for zh, en in [("轮次", "Decision"), ("快照开始 / 结束", "Snapshot start / end"),
                   ("思考开始 / 结束", "Thinking start / end"), ("给出新 trace", "Trace proposed"),
                   ("注入 trace", "Trace injected"), ("决策及结果", "Decision and result")]:
        parts.append(localized(zh, en, "th"))
    parts.append("</tr></thead><tbody>")
    for decision in decisions:
        parts.append(f'<tr><td>{decision["number"]}</td>')
        for lane, (begin, finish) in enumerate(decision["events"]):
            cell = event_cell(begin) + "<br>" + event_cell(finish) if lane < 2 else event_cell(finish)
            parts.append(f"<td>{cell}</td>")
        parts.append(f'<td>{escape(decision["description"])}<br>{escape(decision["result"])}</td></tr>')
    parts.append("</tbody></table></div></details></section>")
    return "\n".join(parts)
