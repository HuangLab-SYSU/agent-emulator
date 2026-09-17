#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
绘制 agent 余额随区块高度变化的系列图(图内文字为英文, 适配论文排版)。

数据: 每轮实验输出的 agent 交易记录 CSV(exp/agentemu-results/round_*/agents/agent-XXX.csv)
  - block_height:  交易所在区块高度
  - balance:       该交易执行后 agent 的余额(原始整数, 超出 int64 需按字符串读入)
  - block_time_ms: 交易所在区块的出块时间(上链时间)毫秒

说明: 所有 agent 初始余额相同(10^36), 余额波动幅度仅数万原始单位,
因此图中统一绘制相对初始余额的变化量 Δbalance = balance - 初始余额;
交易按 (block_height, block_time_ms) 排序, 形成 agent 的余额轨迹,
并用颜色/竖直分界线区分不同区块高度。

用法:
  python3 plot_agent_balance.py
      # 不带参数: 自动选取 exp/agentemu-results/ 下编号最大一轮的 agents/
  python3 plot_agent_balance.py --data-dir <目录> --fig-dir <目录>

绘图逻辑参考 agent_lifecycle_figs/code/plot_agent_balance.py。
"""

import argparse
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
import pandas as pd

# 仓库根目录 = 本脚本上三级(figs/python_code/ -> figs/ -> 仓库根)
REPO_ROOT = Path(__file__).resolve().parents[2]
RESULTS_ROOT = REPO_ROOT / "exp" / "agentemu-results"
DEFAULT_FIG_DIR = REPO_ROOT / "figs" / "figs_results"

Y_LABEL = "Δbalance"
X_LABEL = "Transaction index"


def latest_agents_dir() -> Path:
    """取 exp/agentemu-results/ 下编号最大一轮(round_NNN 零填充, 字典序即轮次序)的 agents/ 目录。"""
    if not RESULTS_ROOT.is_dir():
        raise SystemExit(f"未找到实验结果目录: {RESULTS_ROOT}")
    agents_dirs = [r / "agents" for r in sorted(RESULTS_ROOT.glob("round_*"))
                   if (r / "agents").is_dir()]
    if not agents_dirs:
        raise SystemExit(f"{RESULTS_ROOT} 下没有包含 agents/ 的 round_* 目录")
    return agents_dirs[-1]


# ---------------------------------------------------------------------------
# 全局样式
# ---------------------------------------------------------------------------
def apply_style():
    plt.rcParams.update({
        "font.family": "serif",
        "font.serif": ["Times New Roman", "Times", "STIXGeneral"],
        "mathtext.fontset": "stix",
        "font.size": 18,
        "axes.titlesize": 20,
        "axes.labelsize": 18.5,
        "xtick.labelsize": 17,
        "ytick.labelsize": 17,
        "legend.fontsize": 18,   # 图例放大为原来的 1.5 倍(12 -> 18)
        "axes.unicode_minus": False,
        "figure.dpi": 110,
        "savefig.dpi": 200,
        "savefig.bbox": "tight",
        "axes.grid": True,
        "grid.alpha": 0.3,
        "axes.spines.top": False,
        "axes.spines.right": False,
    })


BLOCK_COLORS = {}  # 每个区块高度一种颜色(main 中按实际高度重建)


def rebuild_block_colors(heights):
    """按数据中实际出现的区块高度重建颜色映射, 避免硬编码高度。"""
    global BLOCK_COLORS
    cmap = plt.get_cmap("tab10")
    BLOCK_COLORS = {h: cmap(i % 10) for i, h in enumerate(sorted(heights))}


# ---------------------------------------------------------------------------
# 数据加载
# ---------------------------------------------------------------------------
def load_agent(path: Path) -> pd.DataFrame:
    """读取单个 agent 的交易记录并按 (区块, 时间) 排序, 计算 Δbalance。"""
    df = pd.read_csv(path, dtype={"balance": str, "value": str})
    df["balance"] = df["balance"].map(int)          # Python 任意精度整数
    df["value"] = df["value"].map(int)
    df = df.sort_values(["block_height", "block_time_ms"], kind="stable").reset_index(drop=True)
    base = int(df.loc[0, "balance"])
    df["delta"] = (df["balance"] - base).astype(float)
    df["agent"] = path.stem                          # e.g. agent-001
    return df


def block_boundary_idx(df: pd.DataFrame) -> int:
    """排序后首个(最低)区块的交易数量, 即第一区块→后续区块的分界下标。"""
    first = int(df["block_height"].iloc[0])
    return int((df["block_height"] == first).sum())


def main():
    parser = argparse.ArgumentParser(description="绘制 agent 余额变化系列图")
    parser.add_argument("--data-dir", type=Path, default=None,
                        help="agent CSV 所在目录(默认自动选取最新一轮实验结果)")
    parser.add_argument("--fig-dir", type=Path, default=DEFAULT_FIG_DIR,
                        help=f"图片输出目录(默认 {DEFAULT_FIG_DIR})")
    args = parser.parse_args()

    data_dir = args.data_dir or latest_agents_dir()
    fig_dir = args.fig_dir
    fig_dir.mkdir(parents=True, exist_ok=True)
    apply_style()

    files = sorted(data_dir.glob("agent-*.csv"))
    if not files:
        raise SystemExit(f"未在 {data_dir} 找到 agent-*.csv")
    dfs = [load_agent(f) for f in files]
    all_df = pd.concat(dfs, ignore_index=True)
    print(f"已加载 {len(dfs)} 个 agent, 共 {len(all_df)} 笔交易, "
          f"区块高度: {sorted(all_df['block_height'].unique())}")
    rebuild_block_colors(all_df["block_height"].unique())
    hs_all = sorted(BLOCK_COLORS)

    # ------------------------------------------------------------------ 图 1
    # 全部 agent 期末余额分布: 按 agent 序号 / 按 Δbalance 排序 两个面板
    fig, (ax_bar_id, ax_bar_sorted) = plt.subplots(1, 2, figsize=(15, 6.5))

    finals = np.array([d["delta"].iloc[-1] for d in dfs])
    ids = [d["agent"].iloc[0] for d in dfs]

    # 按 agent 序号(即文件顺序)的期末余额变化
    ax_bar_id.bar(np.arange(len(finals)), finals,
                  color=np.where(finals >= 0, "#55A868", "#C44E52"))
    ax_bar_id.axhline(0, color="k", lw=0.8)
    ax_bar_id.set_title("Final Δbalance per agent (by agent ID)", fontsize=19)
    ax_bar_id.set_xlabel("Agent ID")
    ax_bar_id.set_ylabel("Final Δbalance")
    id_ticks = np.arange(0, len(finals), 10)
    ax_bar_id.set_xticks(id_ticks)
    ax_bar_id.set_xticklabels([ids[i].replace("agent-", "") for i in id_ticks],
                              fontsize=16.5)

    # 按期末 Δbalance 升序
    ax_bar_sorted.bar(np.arange(len(finals)), np.sort(finals),
                      color=np.where(np.sort(finals) >= 0, "#55A868", "#C44E52"))
    ax_bar_sorted.axhline(0, color="k", lw=0.8)
    ax_bar_sorted.set_title("Distribution of final Δbalance (ascending)",
                            fontsize=19)
    ax_bar_sorted.set_xlabel("Agent")
    ax_bar_sorted.set_ylabel("Final Δbalance")

    # 两个柱状面板统一 y 轴范围, 便于对比
    pad = (finals.max() - finals.min()) * 0.06
    bar_ylim = (finals.min() - pad, finals.max() + pad)
    ax_bar_id.set_ylim(*bar_ylim)
    ax_bar_sorted.set_ylim(*bar_ylim)

    fig.tight_layout()
    fig.savefig(fig_dir / "fig1_all_agents_overview.png")
    plt.close(fig)

    # ------------------------------------------------------------------ 图 2
    # 每 5 个 agent 一组的余额轨迹, 覆盖全部 agent
    group_cmap = plt.get_cmap("tab10")
    n_group_figs = 0
    for g in range(0, len(dfs), 5):
        group = dfs[g:g + 5]
        first_no = group[0]["agent"].iloc[0].replace("agent-", "")
        last_no = group[-1]["agent"].iloc[0].replace("agent-", "")
        fig, ax = plt.subplots(figsize=(13, 7.5))
        for i, d in enumerate(group):
            color = group_cmap(i)
            ax.plot(np.arange(len(d)), d["delta"], color=color, lw=1.1,
                    marker="o", ms=2, label=d["agent"].iloc[0])
            # 该 agent 自己的区块分界线(颜色与轨迹一致) + 分界点星标
            b = block_boundary_idx(d)
            if 0 < b < len(d):
                ax.axvline(b - 0.5, color=color, ls="--", lw=1, alpha=0.45)
                ax.plot([b - 0.5], [d["delta"].iloc[b - 1]], marker="*",
                        ms=11, color=color, mec="white", mew=0.5, zorder=5)
        ax.axhline(0, color="k", lw=0.6, alpha=0.5)
        ax.set_title(f"Agents {first_no}–{last_no}: balance change\n"
                     f"(dashed line / star = own block {hs_all[0]}→{hs_all[1]} "
                     f"boundary)", fontsize=30)
        ax.set_xlabel(X_LABEL, fontsize=27.5)
        ax.set_ylabel(Y_LABEL, fontsize=27.5)
        ax.tick_params(labelsize=25)
        ax.legend(loc="best", ncol=2, fontsize=20)
        fig.tight_layout()
        fig.savefig(fig_dir / f"fig2_agents_{first_no}-{last_no}.png")
        plt.close(fig)
        n_group_figs += 1

    # ------------------------------------------------------------------ 图 3
    # 按相对进度归一化对齐: 每个 agent 的交易进度拉伸到 [0,1],
    # 所有 agent 在每个进度点都有取值(线性插值), 均值曲线终点即全体期末均值
    grid = np.linspace(0.0, 1.0, 201)
    curves = np.empty((len(dfs), len(grid)))
    for i, d in enumerate(dfs):
        xn = np.arange(len(d)) / (len(d) - 1)
        curves[i] = np.interp(grid, xn, d["delta"])
    mean_curve = curves.mean(axis=0)

    fig, ax = plt.subplots(figsize=(12, 6.5))
    ax.plot(grid, curves.T, color="grey", alpha=0.15, lw=0.7)
    ax.plot(grid, mean_curve, color="#C44E52", lw=2.2, label="Mean of all agents")
    ax.axhline(0, color="k", lw=0.6, alpha=0.5)
    ax.annotate(f"Endpoint mean = {mean_curve[-1]:+.0f}", xy=(1.0, mean_curve[-1]),
                xytext=(-12, 16), textcoords="offset points", fontsize=17.5,
                ha="right", color="#C44E52")
    ax.set_xlabel("Transaction progress")
    ax.set_ylabel(Y_LABEL)
    ax.set_title("Balance change aligned by normalized transaction progress",
                 fontsize=20)
    ax.legend(loc="upper left")
    fig.tight_layout()
    fig.savefig(fig_dir / "fig3_normalized_progress.png")
    plt.close(fig)

    # ------------------------------------------------------------------ 图 4
    # 按全局交易顺序统计: 同一笔交易按 tx_hash 去重。注意: 数据只含出块时间戳,
    # 各文件区块内的行序互不一致(存在先后矛盾), 真实执行顺序不可恢复; 这里采用
    # "区块间按高度、区块内按文件序轮转交错"的确定性顺序, 并以 ±value 增量更新
    # 各 agent 的 Δbalance —— 封闭系统下任意顺序的全体均值都恒为 0,
    # 期末值与顺序无关, 仅分布包络的中间形态是示意性的。
    own_addr = {}                             # agent 序号 -> 自身地址(出现在本文件每一行)
    for i, d in enumerate(dfs):
        cand = set(d.loc[0, ["sender", "recipient"]])
        for s_col, r_col in zip(d["sender"], d["recipient"]):
            cand &= {s_col, r_col}
        if len(cand) != 1:
            raise SystemExit(f"{d['agent'].iloc[0]} 无法唯一识别自身地址")
        own_addr[i] = cand.pop()
    addr2idx = {a: i for i, a in own_addr.items()}

    tx_meta = {}                              # tx_hash -> (区块, sender, recipient, value)
    for d in dfs:
        for tx, bh, s, r, v in zip(d["tx_hash"], d["block_height"],
                                    d["sender"], d["recipient"], d["value"]):
            tx_meta.setdefault(tx, (bh, s, r, v))
    emitted = set()
    cur = np.zeros(len(dfs))
    snaps_g, snaps_state = [], []
    for h_block in hs_all:
        seqs = [d.loc[d["block_height"] == h_block, "tx_hash"].tolist() for d in dfs]
        for k in range(max(len(s) for s in seqs)):
            for i, s in enumerate(seqs):
                if k >= len(s) or s[k] in emitted:
                    continue
                tx = s[k]
                emitted.add(tx)
                bh, snd, rcv, v = tx_meta[tx]
                if snd in addr2idx:
                    cur[addr2idx[snd]] -= v
                if rcv in addr2idx:
                    cur[addr2idx[rcv]] += v
                snaps_g.append(len(emitted))
                snaps_state.append(cur.copy())
    finals_diff = max(abs(cur[i] - dfs[i]["delta"].iloc[-1]) for i in range(len(dfs)))
    n_unique = len(emitted)
    states = np.array(snaps_state)            # (全局交易数, agent数)
    g = np.array(snaps_g)
    mean_line = states.mean(axis=1)
    p25, p75 = np.percentile(states, [25, 75], axis=1)
    lo, hi = states.min(axis=1), states.max(axis=1)

    fig, ax = plt.subplots(figsize=(12, 6.5))
    ax.fill_between(g, lo, hi, color="#4C72B0", alpha=0.12, lw=0,
                    label="Min–max range")
    ax.fill_between(g, p25, p75, color="#4C72B0", alpha=0.30, lw=0,
                    label="Interquartile range (P25–P75)")
    ax.plot(g, mean_line, color="#C44E52", lw=2.0, label="Mean of all agents")
    ax.axhline(0, color="k", lw=0.6, alpha=0.5)
    ax.set_xlabel("Global transaction index")
    ax.set_ylabel(Y_LABEL)
    ax.set_title("Distribution of balance changes across agents "
                 "by global transaction order", fontsize=20)
    ax.legend(loc="upper left")
    fig.tight_layout()
    fig.savefig(fig_dir / "fig4_global_tx_order.png")
    plt.close(fig)
    print(f"全局交易序号: 去重后 {n_unique:,} 笔; "
          f"均值曲线最大绝对值 {np.abs(mean_line).max():.3e} (守恒校验); "
          f"期末与各文件末行 Δbalance 最大偏差 {finals_diff:.1f}")

    print(f"已生成 fig1 总览图 + {n_group_figs} 张 fig2 分组图 + fig3 + fig4 到 {fig_dir}")


if __name__ == "__main__":
    main()
