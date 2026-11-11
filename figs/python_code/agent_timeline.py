"""Render the live Agent's timeline from the artifacts of a hybrid run."""

import argparse
import csv
import json
import math
import re
import subprocess
from bisect import bisect_right
from datetime import datetime
from html import escape
from itertools import groupby
from pathlib import Path

COMMIT_LOG = re.compile(r'^time=(\S+) .*?msg="block is added".*?"block height"=(\d+)')
COLORS = ("#087e8b", "#7350a1", "#bb6400", "#227447")


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
    audits = summary.get("decisions") or []
    suppliers = {"agent-gold-supplier": "gold", "agent-silver-supplier": "silver"}
    purchases_by_hash = {}
    for audit in audits:
        decision = audit["reply"]["decision"]
        metal = suppliers.get(decision.get("target"))
        tx_hash = audit.get("transaction_hash")
        if audit["result"] == "accepted" and decision["action"] == "pay" and metal and tx_hash:
            purchases_by_hash[tx_hash] = metal
    ledger = []
    if ledger_path.is_file():
        with ledger_path.open(encoding="utf-8", newline="") as source:
            for row in csv.DictReader(source):
                committed = commits.get((shard, int(row["block_height"])))
                if committed is None:
                    # Legacy ledger timestamps come from the block header (proposal time).
                    commit_times = None
                    committed = int(row["block_time_ms"])
                balance = int(row["balance"])
                metal = purchases_by_hash.get(row["tx_hash"])
                sender = row["sender"].lower().removeprefix("0x")
                if not address or sender != address[-40:].lower():
                    metal = None
                ledger.append((committed, balance, int(row["value"]), metal))
    ledger.sort(key=lambda row: row[0])
    balances = [(time, balance) for time, balance, _, _ in ledger]
    purchases = [
        (time, balance, amount, metal)
        for (time, balance, _, _), (_, _, amount, metal) in zip(ledger, ledger[1:])
        if metal
    ]

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

    final_snapshot = summary["latest_snapshot"]
    reference = final_snapshot["observed_at"]
    zone = parse_time(reference).tzinfo
    snapshots = [audit["observation"] for audit in audits] + [final_snapshot]
    started = next((epoch_ms(obs["observed_at"]) - obs["elapsed_ms"]
                    for obs in snapshots if "elapsed_ms" in obs), None)
    return agent, balances, decisions, commit_times, zone, epoch_ms(reference), started, purchases


def absolute_time(value, zone):
    return datetime.fromtimestamp(value / 1000, zone).isoformat(sep=" ", timespec="milliseconds")


def purchase_callouts(purchases, balances, prices, x, y, left, right, top, bottom):
    """Place labels inside the balance panel, clear of data and other labels."""
    font_size = 15
    padding = 3
    points = [(x(time), y(balance)) for time, balance in balances]
    occupied = []
    label_boxes = []
    for (x1, y1), (x2, y2) in zip(points, points[1:]):
        occupied.extend([
            (x1 - padding, y1 - padding, x2 + padding, y1 + padding),
            (x2 - padding, min(y1, y2) - padding, x2 + padding, max(y1, y2) + padding),
        ])
    last_x, last_y = points[-1]
    occupied.append((last_x, last_y - padding, right, last_y + padding))
    for label, price, _ in prices:
        price_y = y(price)
        price_label = (left + 8, price_y - 26,
                       left + 8 + 8 * len(f"{label}: {price}"), price_y - 3)
        occupied.extend([(left, price_y - padding, right, price_y + padding), price_label])
        label_boxes.append(price_label)

    def overlaps(rect, other):
        a, b, c, d = rect
        e, f, g, h = other
        return a < g and c > e and b < h and d > f

    result = []
    arrows = []

    def crosses(a, b, c, d):
        def side(p, q, r):
            return (q[0] - p[0]) * (r[1] - p[1]) - (q[1] - p[1]) * (r[0] - p[0])
        return side(a, b, c) * side(a, b, d) < 0 and side(c, d, a) * side(c, d, b) < 0

    for (amount, metal), batch in groupby(purchases, key=lambda purchase: purchase[2:]):
        targets = [(x(time), y(balance)) for time, balance, _, _ in batch]
        count = len(targets)
        label = (f"Pay {amount}", f"for {metal}")
        half_width = max(map(len, label)) * font_size * 0.27 + padding
        half_height = 17
        candidates = []
        for cy in range(top + half_height + 1, bottom - half_height, 7):
            for cx in range(math.ceil(left + half_width), math.floor(right - half_width), 8):
                rect = (cx - half_width, cy - half_height, cx + half_width, cy + half_height)
                if any(overlaps(rect, obstacle) for obstacle in occupied):
                    continue
                score = 0
                for px, py in targets:
                    dx, dy = cx - px, cy - py
                    crosses_label = any(
                        a < px + dx * fraction / 10 < c and b < py + dy * fraction / 10 < d
                        for a, b, c, d in label_boxes for fraction in range(1, 10)
                    )
                    crosses_arrow = any(crosses((px, py), (cx, cy), a, b) for a, b in arrows)
                    score += dx * dx + dy * dy + 3500 * crosses_arrow + 1800 * crosses_label
                candidates.append((score, cx, cy))

        if candidates:
            _, cx, cy = min(candidates)
        else:
            middle_x = sum(px for px, _ in targets) / count
            cx = min(right - half_width, max(left + half_width, middle_x))
            cy = bottom - half_height - 2
        rect = (cx - half_width, cy - half_height, cx + half_width, cy + half_height)
        occupied.append(rect)
        label_boxes.append(rect)

        for px, py in targets:
            result.append(f'<circle cx="{px:.2f}" cy="{py:.2f}" r="7" '
                          'fill="none" stroke="#222" stroke-width="1.2"/>')
            distance = math.hypot(cx - px, cy - py) or 1
            ux, uy = (cx - px) / distance, (cy - py) / distance
            edge = min(half_width / abs(ux) if ux else math.inf,
                       half_height / abs(uy) if uy else math.inf)
            tail_x, tail_y = cx - edge * ux, cy - edge * uy
            tip_x, tip_y = px + 7 * ux, py + 7 * uy
            arrows.append(((tail_x, tail_y), (tip_x, tip_y)))
            base_x, base_y = tip_x + 6 * ux, tip_y + 6 * uy
            result.append(f'<path d="M{tail_x:.2f},{tail_y:.2f} L{tip_x:.2f},{tip_y:.2f} '
                          f'M{base_x - 3 * uy:.2f},{base_y + 3 * ux:.2f} L{tip_x:.2f},{tip_y:.2f} '
                          f'L{base_x + 3 * uy:.2f},{base_y - 3 * ux:.2f}" '
                          'fill="none" stroke="#222" stroke-width="1.2"/>')
        for offset, text in zip((-3, 13), label):
            result.append(f'<text font-size="{font_size}" fill="#000000" x="{cx:.2f}" '
                          f'y="{cy + offset:.2f}" text-anchor="middle">{text}</text>')
    return result


def render_chart(agent, balances, decisions, commit_times, zone, end, started, purchases, by_block, prices=()):
    paper_style = not by_block
    width, height = 1100, 455 if paper_style else 408
    left, right = (205 if paper_style else 150), width - 30
    balance_top, balance_bottom = 35, 205
    events_top, events_bottom = 232, 392
    labels = (("Block snapshot", "Agent thinking", "Trace proposal", "Trace injection") if paper_style else
              ("区块快照 / Snapshot", "Agent 思考 / Thinking", "新 trace / Proposed trace", "trace 注入 / Injection"))
    times = [time for time, _ in balances] + [
        time for decision in decisions for interval in decision["events"] for time in interval
        if time is not None
    ] + [end]
    start = 0 if by_block else (started if started is not None else min(times))
    stop = len(commit_times) if by_block else max(times)

    def x(value):
        coordinate = bisect_right(commit_times, value) if by_block else value
        return left + (right - left) * (coordinate - start) / max(stop - start, 1)

    def describe(time):
        blocks = "" if commit_times is None else f" · #{bisect_right(commit_times, time)}"
        return absolute_time(time, zone) + blocks

    svg = [f'<svg viewBox="0 0 {width} {height}" role="img" aria-label="Agent timeline">']
    center = (left + right) / 2 if paper_style else width / 2
    if not paper_style:
        svg.append(f'<text x="{center}" y="16" text-anchor="middle">{escape(agent)} 实验时间线</text>')
    axis_label = "系统累计区块数 / Total committed blocks" if by_block else "Running time (second)"
    axis_y = 443 if paper_style else 403
    svg.append(f'<text x="{center}" y="{axis_y}" text-anchor="middle">{axis_label}</text>')
    if by_block:
        time_ticks = [start + (stop - start) * i // 6 for i in range(7)]
    else:
        step_ms = 1000 * max(1, round((stop - start) / 6000))
        time_ticks = range(start, stop + 1, step_ms)
    for tick in time_ticks:
        tx = left + (right - left) * (tick - start) / max(stop - start, 1)
        label = str(tick) if by_block else str((tick - start) // 1000)
        if paper_style:
            svg.append(f'<path d="M{tx:.2f},{balance_top} V{balance_bottom} '
                       f'M{tx:.2f},{events_top} V{events_bottom}" stroke="#e0e6ed"/>'
                       f'<path d="M{tx:.2f},{events_bottom} v6" stroke="#222"/>'
                       f'<text x="{tx:.2f}" y="416" text-anchor="middle">{label}</text>')
        else:
            svg.append(f'<path d="M{tx:.2f},22 V365" stroke="#e0e6ed"/>'
                       f'<text x="{tx:.2f}" y="385" text-anchor="middle">{label}</text>')

    if paper_style:
        for offset, label in ((90, "Balance of the DeepSeek-"), (71, "connected agent (wei)")):
            svg.append(f'<text font-size="14" fill="#000000" x="0" y="0" text-anchor="middle" '
                       f'transform="matrix(0,1,-1,0,{left - offset},120)">{label}</text>')
    else:
        svg.append(f'<text x="{left - 10}" y="20" text-anchor="end">余额 / Balance</text>')
    if balances:
        low = min(balance for _, balance in balances)
        high = max(balance for _, balance in balances)
        ticks = sorted({low, high})
        if paper_style:
            if prices:
                low = min(low, *(price for _, price, _ in prices))
                high = max(high, *(price for _, price, _ in prices))
            span = max(high - low, 1)
            step = 10 ** (len(str(span)) - 1)
            if span < 2 * step and step >= 10:
                step //= 2
            elif span > 5 * step:
                step *= 2
            low = low // step * step
            high = max(low + step, -(-high // step) * step)
            ticks = range(low, high + 1, step)

        def y(balance):
            if paper_style:
                return balance_bottom - (balance_bottom - balance_top) * (balance - low) / (high - low)
            return 178 - 137 * (balance - low) / max(high - low, 1)

        for balance in ticks:
            label = f"{balance:.3e}" if abs(balance) >= 10**12 else str(balance)
            if paper_style:
                tick_y = y(balance)
                svg.append(f'<path d="M{left},{tick_y:.2f} H{right}" stroke="#e0e6ed"/>'
                           f'<path d="M{left - 6},{tick_y:.2f} H{left}" stroke="#222"/>')
            svg.append(f'<text x="{left - 10}" y="{y(balance) + 4:.2f}" text-anchor="end">{label}</text>')
        if paper_style:
            for label, price, color in prices:
                price_y = y(price)
                svg.append(f'<path d="M{left},{price_y:.2f} H{right}" fill="none" '
                           f'stroke="{color}" stroke-width="1.5" stroke-dasharray="6,4"/>'
                           f'<text font-size="16" fill="{color}" x="{left + 8}" y="{price_y - 7:.2f}">'
                           f'{label}: {price}</text>')
        first_time, first_balance = balances[0]
        steps = [f"M{x(first_time):.2f},{y(first_balance):.2f}"]
        for time, balance in balances[1:]:
            steps.append(f"H{x(time):.2f} V{y(balance):.2f}")
        steps.append(f"H{x(max(times)):.2f}")
        svg.append(f'<path d="{" ".join(steps)}" fill="none" stroke="#245ba2" stroke-width="2"/>')
        for time, balance in balances:
            svg.append(f'<circle cx="{x(time):.2f}" cy="{y(balance):.2f}" r="3.5" fill="#245ba2">'
                       f'<title>{describe(time)} · {balance}</title></circle>')
        if paper_style and purchases:
            svg.extend(purchase_callouts(purchases, balances, prices, x, y, left, right,
                                         balance_top, balance_bottom))

    for lane, (label, color) in enumerate(zip(labels, COLORS)):
        ty = (252 if paper_style else 225) + lane * 40
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
    if paper_style:
        for top, bottom in ((balance_top, balance_bottom), (events_top, events_bottom)):
            svg.append(f'<path d="M{left},{top} H{right} V{bottom} H{left} Z" '
                       'fill="none" stroke="#222" stroke-width="1"/>')
    svg.append("</svg>")
    return "".join(svg)


def rasterize_chart(svg: str, paper_style: bool = False) -> bytes:
    # Supply styles that the browser normally applies to the inline SVG.
    svg = svg.replace('<svg ', '<svg xmlns="http://www.w3.org/2000/svg" ', 1)
    font_size, color = (18, "#000000") if paper_style else (12, "#334155")
    svg = re.sub(r'<text (?!font-size=)', f'<text font-size="{font_size}" fill="{color}" ', svg)
    svg = re.sub(r'(<(?:path|line|circle)\b[^>]*?\bstroke="[^"]+"[^>]*?)(/?>)',
                 r'\1 stroke-opacity="1"\2', svg)
    # MSVG treats nested titles as drawing commands, so drop SVG-only tooltips.
    svg = re.sub(r"<title>.*?</title>", "", svg)
    command = ["magick", "-density", "192", "-background", "white"]
    mac_font = Path("/System/Library/Fonts/Hiragino Sans GB.ttc")
    if mac_font.is_file():
        command.extend(["-font", str(mac_font)])
    try:
        png = subprocess.run([*command, "MSVG:-", "png:-"], input=svg.encode("utf-8"),
                             stdout=subprocess.PIPE, check=True).stdout
    except FileNotFoundError as exc:
        raise SystemExit("ImageMagick (magick) is required to generate the PNG timeline.") from exc
    return png


def main():
    parser = argparse.ArgumentParser(description="Export a hybrid experiment timeline as standalone PNGs")
    parser.add_argument("--data-dir", type=Path, required=True, help="Experiment round's agents directory")
    parser.add_argument("--fig-dir", type=Path, required=True, help="Directory for timeline PNGs")
    parser.add_argument("--gold-price", type=int, help="Gold price line on the elapsed-time chart")
    parser.add_argument("--silver-price", type=int, help="Silver price line on the elapsed-time chart")
    args = parser.parse_args()
    prices = tuple((label, price, color) for label, price, color in (
        ("Gold price", args.gold_price, "#a66a00"),
        ("Silver price", args.silver_price, "#68727c"),
    ) if price is not None)
    if any(price <= 0 for _, price, _ in prices):
        parser.error("Metal prices must be positive integers.")

    data = read_timeline(args.data_dir.parent.parent, args.data_dir)
    if data is None:
        raise SystemExit("No hybrid_summary.json found for this experiment.")
    _, _, _, commit_times, _, _, _, _ = data
    args.fig_dir.mkdir(parents=True, exist_ok=True)
    for by_block, name in ((False, "timeline-relative-time.png"), (True, "timeline-block-count.png")):
        if by_block and commit_times is None:
            print("Block commit history is incomplete. Exported only the elapsed-time timeline.")
            continue
        svg = render_chart(*data, by_block=by_block, prices=prices)
        path = args.fig_dir / name
        path.write_bytes(rasterize_chart(svg, paper_style=not by_block))
        print(f"Generated {path}")


if __name__ == "__main__":
    main()
