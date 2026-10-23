#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""AgentEmulator 沙盒演示验收脚本（港元稳定币微支付购买算力资源场景）。

校验一次实验运行后「审计数据链」是否完整、可信、可追溯：

    trace request_id
      → agent_action_txs.jsonl            （支付意图 → 链上交易哈希）
      → agent_transactions.jsonl          （交易计划：金额、nonce、元数据）
      → chain/results/relay_stats_detail_tx_info.csv（链上确认时间）
      → agents/agent-XXX.csv              （每 agent 余额流水）
      → computing_resources/computing_resources_orders.csv  （算力资源买卖链上记录）

本脚本只做演示前的验收测试（QA），并产出追溯表供监管方查看；
审计动作本身由监管部门执行，本平台不代做（与 docs/computing_resources/plan.md 定位一致）。

用法：
    python scripts/verify_demo_audit.py --data-dir exp/agentemu-results/round_001 --stage full
    python scripts/verify_demo_audit.py --data-dir exp/agentemu-results/round_001 --stage plan

stage=plan 只校验编译产物（不要求链上 CSV，可用于 chain.enabled=false 预览）；
stage=full 额外校验链上确认、账本流水与算力资源记录。退出码 0=全部通过，1=存在 FAIL。
"""

import argparse
import csv
import hashlib
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

MICRO_PER_HKD = 1_000_000
INIT_BALANCE = 10 ** 36  # account.NormalInitBalanceStr，账户首次出现时注入的初始余额

PAYMENT_ACTIONS = {"pay", "buy", "sell"}
ACTION_TO_EVENT = {
    "pay": "pay_onchain",
    "sell": "rwa_sell_onchain",
    "buy": "rwa_buy_onchain",
    "join": "did_register",
    "leave": "did_revoke",
    "raw_tx": "raw_tx",
}

TRAIL_FIELDS = [
    "seq", "action", "request_id", "agent_id", "target", "compute_id", "quantity",
    "amount_micro_hkd", "tx_hash", "confirmed", "rejected", "cross_shard",
    "create_time", "commit_time", "latency_ms",
]


class Check:
    """一项验收检查：PASS / WARN / FAIL，附带明细。"""

    def __init__(self, cid: str, name: str) -> None:
        self.id = cid
        self.name = name
        self.status = "PASS"
        self.failures: list[str] = []
        self.warns: list[str] = []
        self.info: dict = {}

    def fail(self, msg: str) -> None:
        self.status = "FAIL"
        self.failures.append(msg)

    def warn(self, msg: str) -> None:
        if self.status != "FAIL":
            self.status = "WARN"
        self.warns.append(msg)

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "name": self.name,
            "status": self.status,
            "failures": self.failures,
            "warnings": self.warns,
            "info": self.info,
        }


def read_jsonl(path: Path) -> list[dict]:
    rows = []
    with open(path, encoding="utf-8") as f:
        for lineno, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError as exc:
                raise ValueError(f"{path}:{lineno}: 非法 JSON 行: {exc}") from exc
    return rows


def read_csv_rows(path: Path) -> list[dict]:
    with open(path, encoding="utf-8-sig", newline="") as f:
        return list(csv.DictReader(f))


def parse_ts(raw):
    """解析 relay_stats 的 RFC3339 时间；空串返回 None。"""
    raw = (raw or "").strip()
    if not raw:
        return None
    if raw.endswith("Z"):
        raw = raw[:-1] + "+00:00"
    return datetime.fromisoformat(raw)


def is_hex_addr(value, length: int = 40) -> bool:
    v = norm_addr(value)
    return len(v) == length and all(c in "0123456789abcdef" for c in v)


def norm_addr(value) -> str:
    v = str(value or "").strip().lower()
    return v[2:] if v.startswith("0x") else v


def did_addr(did: str) -> str:
    """从 'did:broker:0x<40hex>' 提取 20 字节地址（无 0x 前缀）。"""
    return norm_addr(did.rsplit(":", 1)[-1])


def safe_agent_name(agent_id: str) -> str:
    """与 agentsupervisor.agentFileName 一致的安全文件名映射。"""
    out = []
    for ch in agent_id:
        if ch.isascii() and (ch.isalnum() or ch in ".-_"):
            out.append(ch)
        else:
            out.append("_")
    return "".join(out) + ".csv"


def derive_did(seed: int, agent_id: str) -> str:
    digest = hashlib.sha256(f"{seed}:{agent_id}".encode()).digest()
    return "did:broker:0x" + digest[-20:].hex()


def buy_value(link: dict, quotes: dict):
    """buy 编译金额 = 卖方 (target, compute_id) 报价 × 数量；被拒单为 0；无报价返回 None。"""
    if link.get("rejected"):
        return 0
    price = quotes.get((link.get("target"), link.get("compute_id")))
    if price is None:
        return None
    return price * int(link.get("quantity", 0) or 0)


def sell_quotes(links: list[dict]) -> dict:
    """从 sell 动作收集报价表 {(seller, compute_id): unit_price_int}。"""
    quotes: dict[tuple, int] = {}
    for link in links:
        if link.get("action") == "sell":
            quotes[(link.get("agent_id"), link.get("compute_id"))] = int(link["unit_price"])
    return quotes


# --------------------------------------------------------------------------
# 各项检查
# --------------------------------------------------------------------------

def load_artifacts(checks: list[Check], data_dir: Path, registry_path: Path, stage: str) -> dict:
    """P1/F1: 产物完整性；必需产物缺失时直接终止验收。"""
    base_files = {
        "plan": data_dir / "agent_transactions.jsonl",
        "action_map": data_dir / "agent_action_txs.jsonl",
        "registry": registry_path,
    }
    full_files = {
        "events": data_dir / "Agent_Events.csv",
        "relay_detail": data_dir / "chain" / "results" / "relay_stats_detail_tx_info.csv",
        "rwa_orders": data_dir / "computing_resources" / "computing_resources_orders.csv",
        "rwa_summary": data_dir / "computing_resources" / "computing_resources_summary.csv",
    }
    required = base_files if stage == "plan" else {**base_files, **full_files}

    chk = Check("P1" if stage == "plan" else "F1", "产物完整性")
    checks.append(chk)
    missing = [str(p) for p in required.values() if not p.is_file()]
    for p in missing:
        chk.fail(f"缺少产物: {p}")

    agents_dir = data_dir / "agents"
    agent_csvs = sorted(agents_dir.glob("*.csv")) if agents_dir.is_dir() else []
    chk.info["agent_csv_count"] = len(agent_csvs)
    if stage == "full" and not agent_csvs:
        chk.fail(f"缺少每 agent 余额流水目录: {agents_dir}")

    if missing or (stage == "full" and not agent_csvs):
        for f in chk.failures:
            print(f"[✗ FAIL] {chk.id} {chk.name}: {f}")
        raise SystemExit(
            "产物不完整，无法继续验收（见上方 FAIL 项）。"
            "若指向归档副本，归档机制不保留 CSV，请用 run_agentemu 全流程重新生成数据。"
        )

    loaded = {}
    for key, p in {**base_files, **full_files}.items():
        if not p.is_file():
            continue
        if p.suffix == ".jsonl":
            loaded[key] = read_jsonl(p)
        elif p.suffix == ".json":
            loaded[key] = json.loads(p.read_text(encoding="utf-8"))
        else:
            loaded[key] = read_csv_rows(p)
    loaded["agent_csv_files"] = agent_csvs
    return loaded


def check_plan_integrity(plan: list[dict], checks: list[Check]) -> dict:
    """P2: 交易计划自身完整（哈希唯一、金额可解析、nonce 连续）。"""
    chk = Check("P2", "交易计划完整性（哈希/金额/nonce）")
    checks.append(chk)

    seen: set[str] = set()
    nonce_by_sender: dict[str, int] = {}
    for i, line in enumerate(plan):
        h = line.get("hash", "")
        if len(h) != 64 or any(c not in "0123456789abcdef" for c in h):
            chk.fail(f"plan 第 {i + 1} 行 hash 非法: {h!r}")
        if h in seen:
            chk.fail(f"plan 第 {i + 1} 行 hash 重复: {h}")
        seen.add(h)

        if not is_hex_addr(line.get("sender")):
            chk.fail(f"plan 第 {i + 1} 行 sender 非法: {line.get('sender')!r}")
        if line.get("rwa_action") == "sell":
            if line.get("recipient", "") != "":
                chk.fail(f"plan 第 {i + 1} 行 sell 的 recipient 应为空")
            if line.get("value", "") != "":
                chk.fail(f"plan 第 {i + 1} 行 sell 的 value 应为空，实际 {line.get('value')!r}")
        else:
            if not is_hex_addr(line.get("recipient")):
                chk.fail(f"plan 第 {i + 1} 行 recipient 非法: {line.get('recipient')!r}")
            try:
                if int(line.get("value", "")) < 0:
                    raise ValueError
            except ValueError:
                chk.fail(f"plan 第 {i + 1} 行 value 非法: {line.get('value')!r}")

        sender = norm_addr(line.get("sender"))
        try:
            nonce = int(line.get("nonce"))
        except (TypeError, ValueError):
            chk.fail(f"plan 第 {i + 1} 行 nonce 非法: {line.get('nonce')!r}")
            continue
        expected = nonce_by_sender.get(sender, 0)
        if nonce != expected:
            chk.fail(f"sender 0x{sender} 的 nonce 不连续: 期望 {expected}，实际 {nonce}")
        nonce_by_sender[sender] = nonce + 1

    chk.info["tx_count"] = len(plan)
    chk.info["senders"] = len(nonce_by_sender)
    return {"hashes": seen, "by_hash": {line.get("hash"): line for line in plan}}


def check_action_map(links: list[dict], plan_index: dict, checks: list[Check]) -> None:
    """P3: 每条 trace 动作都链接到链上交易，且哈希能对上交易计划。"""
    chk = Check("P3", "支付意图 ↔ 链上交易哈希关联完整")
    checks.append(chk)

    assigned: dict[str, int] = {}
    for i, link in enumerate(links):
        action = link.get("action")
        hashes = link.get("tx_hashes") or []
        if action in PAYMENT_ACTIONS and len(hashes) != 1:
            chk.fail(
                f"link[{i}] action={action} request_id={link.get('request_id')!r} "
                f"应恰好编译为 1 笔交易，实际 {len(hashes)} 笔"
            )
        for h in hashes:
            if h in assigned:
                chk.fail(f"tx hash {h} 同时归属 link[{assigned[h]}] 与 link[{i}]")
            assigned[h] = i
            if h not in plan_index["by_hash"]:
                chk.fail(f"link[{i}] 的 tx hash {h} 不在交易计划中")
    chk.info["action_count"] = len(links)
    chk.info["linked_tx_count"] = len(assigned)


def check_request_ids(links: list[dict], checks: list[Check]) -> None:
    """P4: 支付类 request_id 全局唯一（审计关联的主键）。"""
    chk = Check("P4", "request_id 唯一性")
    checks.append(chk)

    seen: dict[str, int] = {}
    for i, link in enumerate(links):
        rid = (link.get("request_id") or "").strip()
        action = link.get("action")
        if not rid:
            if action in PAYMENT_ACTIONS:
                chk.fail(f"link[{i}] action={action} 缺少 request_id")
            continue
        if rid in seen:
            if action in PAYMENT_ACTIONS:
                chk.fail(f"request_id {rid!r} 重复出现（link[{seen[rid]}] 与 link[{i}]）")
            else:
                chk.warn(f"request_id {rid!r} 在 action={action} 中重复（生命周期动作允许复用）")
        seen[rid] = i
    chk.info["unique_request_ids"] = len(seen)


def check_pricing(links: list[dict], plan_index: dict, checks: list[Check]) -> None:
    """P5: buy 成交额 = 卖方报价 × 数量，且链上元数据与 trace 一致。"""
    chk = Check("P5", "算力资源定价与元数据一致性（value = unit_price × quantity）")
    checks.append(chk)

    quotes: dict[tuple, dict] = {}
    for link in links:
        if link.get("action") != "sell":
            continue
        key = (link.get("agent_id"), link.get("compute_id"))
        price = int(link["unit_price"])
        if key in quotes and int(quotes[key]["unit_price"]) != price:
            chk.warn(f"报价 {key} 重复且价格不同（{quotes[key]['unit_price']} → {price}），以最后一条为准")
        quotes[key] = link

    payment_total = 0
    buy_count = 0
    rejected_count = 0
    for link in links:
        if link.get("action") != "buy":
            continue
        if link.get("rejected"):
            rejected_count += 1
            # 被拒单不转账：编译为零值记录型交易，跳过报价校验，仅断言零值。
            for h in link.get("tx_hashes") or []:
                line = plan_index["by_hash"].get(h)
                if line is not None and line.get("value", "") != "0":
                    chk.fail(f"被拒 buy request_id={link.get('request_id')!r} 应为零值交易，实际 value={line.get('value')!r}")
            continue
        buy_count += 1
        key = (link.get("target"), link.get("compute_id"))
        quote = quotes.get(key)
        if quote is None:
            chk.fail(f"buy request_id={link.get('request_id')!r} 找不到卖方报价: seller={key[0]!r} compute_id={key[1]!r}")
            continue
        try:
            quantity = int(link.get("quantity", 0))
        except (TypeError, ValueError):
            chk.fail(f"buy request_id={link.get('request_id')!r} quantity 非法: {link.get('quantity')!r}")
            continue
        expected = int(quote["unit_price"]) * quantity
        for h in link.get("tx_hashes") or []:
            line = plan_index["by_hash"].get(h)
            if line is None:
                continue
            try:
                actual = int(line.get("value", ""))
            except ValueError:
                chk.fail(f"buy request_id={link.get('request_id')!r} 计划交易 value 非法: {line.get('value')!r}")
                continue
            if actual != expected:
                chk.fail(
                    f"buy request_id={link.get('request_id')!r} 金额不符: 期望 "
                    f"unit_price({quote['unit_price']}) × quantity({quantity}) = {expected}，实际 {actual}"
                )
            for field, link_key in (
                ("request_id", "request_id"),
                ("agent_id", "agent_id"),
                ("target_id", "target"),
                ("compute_id", "compute_id"),
            ):
                if str(line.get(field, "")) != str(link.get(link_key, "")):
                    chk.fail(
                        f"buy request_id={link.get('request_id')!r} 计划交易 {field}="
                        f"{line.get(field)!r} 与 trace {link.get(link_key)!r} 不一致"
                    )
        payment_total += expected
    chk.info["buy_count"] = buy_count
    chk.info["rejected_buy_count"] = rejected_count
    chk.info["buy_total_micro_hkd"] = payment_total


def check_registry(registry: list[dict], seed, checks: list[Check]) -> dict:
    """P6: DID 由 seed+agent_id 确定性派生，可复现、可验证。"""
    chk = Check("P6", "DID 确定性派生（seed + agent_id）")
    checks.append(chk)

    addr_to_agent: dict[str, str] = {}
    by_id = {entry.get("agent_id"): entry for entry in registry}
    if seed is None:
        chk.warn("缺少 seed（rounds_summary.json 缺失且未传 --seed），跳过 DID 复算")
    for entry in registry:
        agent_id = entry.get("agent_id", "")
        did = entry.get("did", "")
        if not is_hex_addr(did_addr(did)):
            chk.fail(f"agent {agent_id!r} DID 地址非法: {did!r}")
            continue
        addr_to_agent[did_addr(did)] = agent_id
        if seed is not None:
            expected = derive_did(int(seed), agent_id)
            if did != expected:
                chk.fail(f"agent {agent_id!r} DID 不符: 期望 {expected}，实际 {did}")
    if seed is not None:
        chk.info["agents"] = len(registry)
        chk.info["active"] = sum(1 for e in registry if e.get("active"))
    return {"addr_to_agent": addr_to_agent, "by_id": by_id}


def check_events(data: dict, links: list[dict], checks: list[Check], required: bool) -> None:
    """P7: Agent_Events.csv 与动作一一对应。"""
    chk = Check("P7", "Agent_Events 与 trace 动作对应")
    checks.append(chk)
    events = data.get("events")
    if events is None:
        if required:
            chk.fail("缺少 Agent_Events.csv")
        else:
            chk.warn("缺少 Agent_Events.csv（归档副本常见），跳过对应性检查")
        return
    if len(events) != len(links):
        chk.fail(f"事件数 {len(events)} 与动作数 {len(links)} 不一致")
    else:
        for i, (event, link) in enumerate(zip(events, links)):
            if link.get("action") == "buy" and link.get("rejected"):
                expected = "rwa_buy_rejected"
            else:
                expected = ACTION_TO_EVENT.get(link.get("action", ""))
            if expected and event.get("kind") != expected:
                chk.fail(f"第 {i + 1} 条事件 kind={event.get('kind')!r} 与动作 {link.get('action')!r} 不匹配（期望 {expected}）")
    chk.info["event_count"] = len(events)


def confirmation_index(data: dict) -> dict:
    """OriginalHash → relay_stats 行（同哈希多行时取 commit 时间最新的一行）。"""
    by_hash: dict[str, dict] = {}
    for row in data.get("relay_detail") or []:
        h = norm_addr(row.get("OriginalHash"))
        if not h:
            continue
        prev = by_hash.get(h)
        if prev is None or (parse_ts(row.get("Tx finally commit time")) or datetime.min.replace(tzinfo=timezone.utc)) > (
            parse_ts(prev.get("Tx finally commit time")) or datetime.min.replace(tzinfo=timezone.utc)
        ):
            by_hash[h] = row
    return by_hash


def check_chain_confirmation(plan_index: dict, links: list[dict], data: dict, checks: list[Check]) -> None:
    """F2/F3: 提交率与支付全链路可追溯。"""
    chk_commit = Check("F2", "链上提交率（交易计划全部确认）")
    chk_trace = Check("F3", "request_id → tx_hash → 链上确认 全链路可追溯")
    checks.append(chk_commit)
    checks.append(chk_trace)

    by_hash = confirmation_index(data)
    chk_commit.info["detail_rows"] = len(data.get("relay_detail") or [])
    chk_commit.info["unique_hashes"] = len(by_hash)

    missing = []
    for h in sorted(plan_index["hashes"]):
        row = by_hash.get(h)
        if row is None or parse_ts(row.get("Tx finally commit time")) is None:
            missing.append(h)
    total = len(plan_index["hashes"])
    chk_commit.info["committed"] = total - len(missing)
    chk_commit.info["total"] = total
    if missing:
        for h in missing[:10]:
            chk_commit.fail(f"交易 {h} 未确认（无记录或无 commit time）")
        if len(missing) > 10:
            chk_commit.fail(f"……另有 {len(missing) - 10} 笔未确认")
    else:
        chk_commit.info["commit_rate"] = "100%"

    latencies = []
    unconfirmed = []
    for link in links:
        action = link.get("action")
        if action not in ("pay", "buy"):
            continue
        for h in link.get("tx_hashes") or []:
            row = by_hash.get(h.lower())
            create = parse_ts((row or {}).get("Tx create time"))
            commit = parse_ts((row or {}).get("Tx finally commit time"))
            if commit is None:
                unconfirmed.append(f"{action}:{link.get('request_id')}")
            elif create and not link.get("rejected"):
                latencies.append(int((commit - create).total_seconds() * 1000))
    if unconfirmed:
        for item in unconfirmed[:10]:
            chk_trace.fail(f"支付动作未获链上确认: {item}")
    else:
        payments = [l for l in links if l.get("action") in ("pay", "buy")]
        chk_trace.info["traceable_payments"] = f"{len(payments)}/{len(payments)}"
    if latencies:
        chk_trace.info["payment_latency_ms"] = {"avg": round(sum(latencies) / len(latencies)), "max": max(latencies)}


def check_ledger(data: dict, registry_index: dict, links: list[dict], plan_index: dict, checks: list[Check]) -> None:
    """F4: 每 agent 账本流水连续、支付双侧金额一致、全局守恒。"""
    chk = Check("F4", "账本审计（流水连续/双侧一致/守恒）")
    checks.append(chk)

    addr_to_agent = registry_index["addr_to_agent"]
    agent_addr = {aid: did_addr(e.get("did", "")) for aid, e in registry_index["by_id"].items()}

    total_debit = 0
    total_credit = 0
    rows_by_agent: dict[str, list[dict]] = {}
    for path in data["agent_csv_files"]:
        agent_id = path.stem
        if agent_id not in agent_addr:
            chk.fail(f"agents/ 下出现注册表之外的文件: {path.name}")
            continue
        rows = read_csv_rows(path)
        rows_by_agent[agent_id] = rows
        mine = agent_addr[agent_id]
        prev = INIT_BALANCE
        for i, row in enumerate(rows):
            try:
                value = int(row.get("value", ""))
                balance = int(row.get("balance", ""))
            except ValueError:
                chk.fail(f"{path.name} 第 {i + 1} 行 value/balance 非法: {row.get('value')!r}/{row.get('balance')!r}")
                continue
            recipient = norm_addr(row.get("recipient"))
            sender = norm_addr(row.get("sender"))
            if recipient == mine:
                delta = value
            elif sender == mine:
                delta = -value
            else:
                chk.fail(f"{path.name} 第 {i + 1} 行与本人地址 {mine} 无关")
                continue
            expected = prev + delta
            if balance != expected:
                chk.fail(f"{path.name} 第 {i + 1} 行余额不连续: 期望 {expected}，实际 {balance}（前值 {prev}，delta {delta:+d}）")
            prev = balance
            if delta < 0:
                total_debit += value
            else:
                total_credit += value

    chk.info["ledger_agents"] = len(rows_by_agent)
    chk.info["total_debit_micro_hkd"] = total_debit
    chk.info["total_credit_micro_hkd"] = total_credit
    if total_debit != total_credit:
        chk.fail(f"全局守恒不成立: 总借方 {total_debit} ≠ 总贷方 {total_credit}")

    checked = 0
    for link in links:
        if link.get("action") not in ("pay", "buy"):
            continue
        for h in link.get("tx_hashes") or []:
            line = plan_index["by_hash"].get(h)
            if line is None:
                continue
            sender_agent = addr_to_agent.get(norm_addr(line.get("sender")))
            recipient_agent = addr_to_agent.get(norm_addr(line.get("recipient")))
            value = int(line.get("value", "0"))
            if not _ledger_has_row(rows_by_agent, sender_agent, h, value, sender=norm_addr(line.get("sender"))):
                chk.fail(f"request_id={link.get('request_id')!r} 付款方 {sender_agent!r} 账本缺少扣款行 (tx {h[:16]}…)")
            if not _ledger_has_row(rows_by_agent, recipient_agent, h, value, recipient=norm_addr(line.get("recipient"))):
                chk.fail(f"request_id={link.get('request_id')!r} 收款方 {recipient_agent!r} 账本缺少入账行 (tx {h[:16]}…)")
            checked += 1
    chk.info["checked_payment_pairs"] = checked


def _ledger_has_row(rows_by_agent: dict, agent_id, tx_hash: str, value: int, sender=None, recipient=None) -> bool:
    if agent_id is None:
        return False
    for row in rows_by_agent.get(agent_id, []):
        if row.get("tx_hash") != tx_hash or row.get("value", "") != str(value):
            continue
        if sender is not None and norm_addr(row.get("sender")) == sender:
            return True
        if recipient is not None and norm_addr(row.get("recipient")) == recipient:
            return True
    return False


def check_rwa_orders(data: dict, links: list[dict], checks: list[Check]) -> None:
    """F5: 每条 buy/sell 都有对应的链上 RWA 记录，金额一致。"""
    chk = Check("F5", "算力资源链上记录与 trace 一致")
    checks.append(chk)
    orders = data.get("rwa_orders") or []
    order_index: dict[tuple, list[dict]] = {}
    for row in orders:
        order_index.setdefault((row.get("rwa_action"), row.get("request_id")), []).append(row)

    insufficient = []
    for link in links:
        action = link.get("action")
        if action not in ("buy", "sell"):
            continue
        matched = order_index.get((action, link.get("request_id")))
        if not matched:
            chk.fail(f"链上缺少算力资源记录: action={action} request_id={link.get('request_id')!r}")
            continue
        if action == "buy":
            expected = link.get("_value")
            if expected is not None and any(row.get("value", "") != str(expected) for row in matched):
                chk.fail(f"buy request_id={link.get('request_id')!r} 链上 value 与编译金额不一致")
        if link.get("rejected"):
            if not all((row.get("rejected") or "").strip().lower() == "true" for row in matched):
                chk.fail(f"被拒 buy request_id={link.get('request_id')!r} 链上记录缺少 rejected=true 标记")
        elif any((row.get("rejected") or "").strip().lower() == "true" for row in matched):
            chk.fail(f"成交 buy request_id={link.get('request_id')!r} 链上记录被误标为 rejected")
        if any((row.get("insufficient_balance") or "").strip().lower() == "true" for row in matched):
            insufficient.append(link.get("request_id"))
    chk.info["rwa_order_rows"] = len(orders)
    chk.info["insufficient_balance_requests"] = insufficient
    if insufficient:
        chk.warn(
            f"{len(insufficient)} 笔 buy 余额不足仍上链（V1.0 特性：记录意图并打标 insufficient_balance）: "
            f"{insufficient[:5]}"
        )


def check_logs(data_dir: Path, checks: list[Check]) -> None:
    """F6: 链日志卫生——ERROR 判 FAIL；WARN 提示供演示前人工复核。"""
    chk = Check("F6", "链日志卫生（无 Error / Warn）")
    checks.append(chk)
    log_dir = data_dir / "chain" / "logs"
    if not log_dir.is_dir():
        chk.warn(f"缺少日志目录 {log_dir}，跳过日志检查")
        return
    errors, warns, scanned = [], [], 0
    for path in sorted(log_dir.rglob("*")):
        if not path.is_file():
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError as exc:
            chk.warn(f"读取日志失败 {path}: {exc}")
            continue
        scanned += 1
        for lineno, line in enumerate(text.splitlines(), 1):
            low = line.lower()
            if "level=error" in low or '"level":"error"' in low:
                errors.append(f"{path.name}:{lineno}: {line[:200]}")
            elif "level=warn" in low or '"level":"warn"' in low:
                warns.append(f"{path.name}:{lineno}: {line[:200]}")
    chk.info["log_files_scanned"] = scanned
    chk.info["error_count"] = len(errors)
    chk.info["warn_count"] = len(warns)
    for item in errors[:10]:
        chk.fail(f"日志存在 ERROR: {item}")
    for item in warns[:10]:
        chk.warn(f"日志存在 WARN: {item}")


# --------------------------------------------------------------------------
# 追溯表输出
# --------------------------------------------------------------------------

def build_trail(links: list[dict], data: dict) -> list[dict]:
    """监管可读的追溯表：每条 trace 动作 → 链上交易 → 确认时间/延迟。"""
    by_hash = confirmation_index(data)
    rows = []
    for link in links:
        amount = 0
        if link.get("action") == "pay":
            amount = int(link.get("amount", 0) or 0)
        elif link.get("action") == "buy":
            amount = int(link.get("_value", 0) or 0)
        for h in link.get("tx_hashes") or []:
            row = by_hash.get(h.lower()) or {}
            create = parse_ts(row.get("Tx create time"))
            commit = parse_ts(row.get("Tx finally commit time"))
            latency = int((commit - create).total_seconds() * 1000) if commit and create else ""
            rows.append(
                {
                    "seq": link.get("seq"),
                    "action": link.get("action"),
                    "request_id": link.get("request_id", ""),
                    "agent_id": link.get("agent_id", ""),
                    "target": link.get("target", ""),
                    "compute_id": link.get("compute_id", ""),
                    "quantity": link.get("quantity", ""),
                    "amount_micro_hkd": amount,
                    "tx_hash": h,
                    "confirmed": "yes" if commit else "no",
                    "rejected": "yes" if link.get("rejected") else "no",
                    "cross_shard": row.get("Is cross-shard tx or not", ""),
                    "create_time": row.get("Tx create time", ""),
                    "commit_time": row.get("Tx finally commit time", ""),
                    "latency_ms": latency,
                }
            )
    return rows


def write_trail(path: Path, rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8-sig", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=TRAIL_FIELDS)
        writer.writeheader()
        writer.writerows(rows)


# --------------------------------------------------------------------------
# 主流程
# --------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(description="AgentEmulator 沙盒演示验收脚本")
    parser.add_argument("--data-dir", default="exp/agentemu-results/round_001", help="round 输出目录")
    parser.add_argument("--stage", choices=["plan", "full"], default="full", help="校验深度")
    parser.add_argument("--registry", default=None, help="agent_registry.json 路径（默认取 round 目录的上一级）")
    parser.add_argument("--seed", type=int, default=None, help="实验 seed（默认读 rounds_summary.json）")
    parser.add_argument("--out", default=None, help="audit_report.json 输出路径")
    parser.add_argument("--trail", default=None, help="audit_trail.csv 输出路径")
    args = parser.parse_args()

    data_dir = Path(args.data_dir)
    registry_path = Path(args.registry) if args.registry else data_dir.parent / "agent_registry.json"

    checks: list[Check] = []
    data = load_artifacts(checks, data_dir, registry_path, args.stage)

    plan, links, registry = data["plan"], data["action_map"], data["registry"]
    plan_index = check_plan_integrity(plan, checks)
    check_action_map(links, plan_index, checks)
    check_request_ids(links, checks)
    check_pricing(links, plan_index, checks)

    quotes = sell_quotes(links)
    for link in links:
        if link.get("action") == "buy":
            value = buy_value(link, quotes)
            link["_value"] = value if value is not None else 0

    seed = args.seed
    trace_name = None
    summary_path = data_dir.parent / "rounds_summary.json"
    if summary_path.is_file():
        try:
            summary_json = json.loads(summary_path.read_text(encoding="utf-8"))
            seed = seed if seed is not None else summary_json.get("seed")
            trace_name = summary_json.get("trace")
        except (json.JSONDecodeError, OSError):
            pass
    registry_index = check_registry(registry, seed, checks)
    check_events(data, links, checks, required=(args.stage == "full"))

    payment_total = sum(
        int(l.get("amount", 0) or 0) if l.get("action") == "pay" else int(l.get("_value", 0) or 0)
        for l in links
        if l.get("action") in ("pay", "buy")
    )
    rejected = sum(1 for l in links if l.get("rejected"))
    summary = {
        "actions": len(links),
        "txs_planned": len(plan),
        "rejected_buys": rejected,
        "payment_total_micro_hkd": payment_total,
    }

    if args.stage == "full":
        check_chain_confirmation(plan_index, links, data, checks)
        check_ledger(data, registry_index, links, plan_index, checks)
        check_rwa_orders(data, links, checks)
        check_logs(data_dir, checks)

    # 控制台摘要
    print("=" * 72)
    print(f"AgentEmulator 沙盒演示验收  stage={args.stage}  data_dir={data_dir}")
    print(f"trace={trace_name}  seed={seed}  actions={summary['actions']}  plan_txs={summary['txs_planned']}  rejected_buys={summary['rejected_buys']}")
    print(f"支付总额 = {payment_total} microHKD（≈ {payment_total / MICRO_PER_HKD:.2f} HKD）")
    print("-" * 72)
    failed = 0
    for chk in checks:
        mark = {"PASS": "✓ PASS", "WARN": "⚠ WARN", "FAIL": "✗ FAIL"}[chk.status]
        if chk.status == "FAIL":
            failed += 1
        print(f"[{mark}] {chk.id} {chk.name}")
        for key, val in chk.info.items():
            print(f"         {key}: {val}")
        for w in chk.warns:
            print(f"         ⚠ {w}")
        for f in chk.failures:
            print(f"         ✗ {f}")
    print("=" * 72)

    report = {
        "generated_at_utc": datetime.now(timezone.utc).isoformat(),
        "data_dir": str(data_dir),
        "stage": args.stage,
        "seed": seed,
        "trace": trace_name,
        "summary": summary,
        "overall": "PASS" if failed == 0 else "FAIL",
        "checks": [chk.to_dict() for chk in checks],
    }
    out_path = Path(args.out) if args.out else data_dir / "audit_report.json"
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"验收报告: {out_path}")

    if args.stage == "full":
        trail_path = Path(args.trail) if args.trail else data_dir / "audit_trail.csv"
        trail_rows = build_trail(links, data)
        if trail_rows:
            write_trail(trail_path, trail_rows)
            print(f"监管追溯表: {trail_path}（{len(trail_rows)} 行）")

    overall = "PASS" if failed == 0 else "FAIL"
    print(f"总体结论: {overall}（{failed} 项 FAIL）")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
