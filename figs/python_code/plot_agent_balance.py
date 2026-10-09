#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
绘制 agent 余额变化的系列图(图内文字为英文, 字体 Times New Roman, 适配论文排版)。

数据: 每轮实验输出的 agent 交易记录 CSV(exp/agentemu-results/round_*/agents/agent-XXX.csv)
  - block_height:  交易所在区块高度
  - balance:       该交易执行后 agent 的余额(原始整数, 超出 int64 需按字符串读入)
  - block_time_ms: 交易所在区块的出块时间(上链时间)毫秒

说明: 所有 agent 初始余额相同, 余额波动幅度仅数万原始单位,
因此图中统一绘制相对初始余额的变化量 Δbalance = balance - 初始余额。

输出(figs/figs_results/, PNG 供 HTML 图册引用, PDF 供论文排版):
  - 图 1  fig1_all_agents_overview:  按 agent 序号排列的期末余额变化柱状图(单面板)
  - 图 2  fig2_global_tx_order:      全局交易序号下的 min–max / P25–P75 / 全体均值曲线
  - 图 3  fig3_normalized_progress:  按交易处理进度归一化对齐的余额变化(含均值曲线)
  - 图 4  fig4_agents_XXX-YYY:       每 5 个 agent 一组的余额轨迹, 覆盖全部 agent

用法:
  python3 plot_agent_balance.py
      # 不带参数: 自动选取 exp/agentemu-results/ 下编号最大一轮的 agents/
  python3 plot_agent_balance.py --data-dir <目录> --fig-dir <目录>
"""

import argparse
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.ticker import PercentFormatter
from matplotlib.transforms import ScaledTranslation
import numpy as np
import pandas as pd

# 仓库根目录 = 本脚本上三级(figs/python_code/ -> figs/ -> 仓库根)
REPO_ROOT = Path(__file__).resolve().parents[2]
RESULTS_ROOT = REPO_ROOT / "exp" / "agentemu-results"
DEFAULT_FIG_DIR = REPO_ROOT / "figs" / "figs_results"

Y_LABEL = "Balance change from initial value (wei)"
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
        "font.family": "Times New Roman",
        "mathtext.fontset": "stix",          # 数学字体与 Times New Roman 最接近
        "font.size": 24,
        "axes.titlesize": 20,
        "axes.labelsize": 24,
        "xtick.labelsize": 22,
        "ytick.labelsize": 22,
        "legend.fontsize": 22,
        "axes.unicode_minus": False,
        "figure.dpi": 300,
        "savefig.dpi": 600,
        "savefig.bbox": "tight",
        "axes.grid": True,
        "grid.alpha": 0.3,
        "axes.spines.top": True,
        "axes.spines.right": True,
        "pdf.fonttype": 42,                  # PDF 内嵌 TrueType, 文字可编辑/可检索
    })


def save_fig(fig, fig_dir: Path, stem: str):
    """同一张图同时输出 PNG(HTML 图册用)与 PDF(论文排版用)。"""
    for out in (fig_dir / f"{stem}.png", fig_dir / f"{stem}.pdf"):
        fig.savefig(out)
        print(f"已生成 {out}")


# ---------------------------------------------------------------------------
# 数据加载
# ---------------------------------------------------------------------------
def load_agent(path: Path) -> pd.DataFrame:
    """读取单个 agent 的交易记录并按 (区块, 时间) 排序, 计算 Δbalance。"""
    # sender/recipient 是不带 0x 的十六进制地址; 纯数字地址(如 DID 合约
    # 000..030)必须按字符串读, 否则会被推断成整数, 破坏地址等值比较。
    df = pd.read_csv(path, dtype={"balance": str, "value": str,
                                  "sender": str, "recipient": str})
    df["balance"] = df["balance"].map(int)          # Python 任意精度整数
    df["value"] = df["value"].map(int)
    df = df.sort_values(["block_height", "block_time_ms"], kind="stable").reset_index(drop=True)
    base = int(df.loc[0, "balance"])
    df["delta"] = (df["balance"] - base).astype(float)
    df["agent"] = path.stem                          # e.g. agent-001
    return df


# ---------------------------------------------------------------------------
# 图 2 统计: 按全局交易顺序回放
# ---------------------------------------------------------------------------
def replay_global_order(dfs, hs_all):
    """按 "区块间按高度、区块内按文件序轮转交错" 的确定性全局顺序回放交易。

    注意: 数据只含出块时间戳, 各文件区块内的行序互不一致(存在先后矛盾),
    真实执行顺序不可恢复; 这里以 ±value 增量更新各 agent 的 Δbalance ——
    封闭系统下任意顺序的全体均值都恒为 0, 期末值与顺序无关,
    仅分布包络的中间形态是示意性的。

    返回 (g, states, n_unique):
      g        每个快照对应的全局交易序号(从 1 开始)
      states   各快照下全部 agent 的 Δbalance, 形状 (全局交易数, agent数)
      n_unique 去重后的全局交易笔数
    """
    own_addr = {}                             # agent 序号 -> 自身地址(出现在本文件每一行)
    # 出现在所有 agent 文件中的地址是共享合约(如 DID 注册合约 0x..30), 不属于任何
    # agent; 只加入不支付的观察员每行都含该合约地址, 交集会出现两个候选, 需排除。
    addr_file_cnt = {}
    for d in dfs:
        for a in set(d["sender"]) | set(d["recipient"]):
            addr_file_cnt[a] = addr_file_cnt.get(a, 0) + 1
    shared_everywhere = ({a for a, c in addr_file_cnt.items() if c == len(dfs)}
                         if len(dfs) > 1 else set())
    for i, d in enumerate(dfs):
        cand = set(d.loc[0, ["sender", "recipient"]])
        for s_col, r_col in zip(d["sender"], d["recipient"]):
            cand &= {s_col, r_col}
        cand -= shared_everywhere
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
    return np.array(snaps_g), np.array(snaps_state), len(emitted)


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
    hs_all = sorted(all_df["block_height"].unique())

    finals = np.array([d["delta"].iloc[-1] for d in dfs])
    ids = [d["agent"].iloc[0] for d in dfs]

    # ------------------------------------------------------------------ 图 1
    # 全部 agent 期末余额变化: 按 agent 序号(即文件顺序)排列的柱状图
    fig, ax = plt.subplots(figsize=(10, 6.5))
    ax.bar(np.arange(len(finals)), finals,
           color=np.where(finals >= 0, "#55A868", "#C44E52"))
    ax.axhline(0, color="k", lw=0.8)
    ax.set_xlabel("Agent ID")
    ax.set_ylabel(Y_LABEL)
    id_ticks = [0] + list(range(9, len(finals), 10))   # 对应 agent 001, 010, 020, ...
    ax.set_xticks(id_ticks)
    ax.set_xticklabels([ids[i].replace("agent-", "") for i in id_ticks],
                       fontsize=22)

    # y 轴上下各留 6% 余量; x 轴两侧仅留 1% 边距
    pad = (finals.max() - finals.min()) * 0.06
    ax.set_ylim(finals.min() - pad, finals.max() + pad)
    ax.margins(x=0.01)
    ax.ticklabel_format(style="sci", axis="y", scilimits=(0, 0))   # 纵轴科学计数法

    fig.tight_layout()
    save_fig(fig, fig_dir, "fig1_all_agents_overview")
    plt.close(fig)

    # ------------------------------------------------------------------ 图 2
    # 全局交易序号下的余额变化: min–max 范围 / 四分位距 (P25–P75) / 全体均值曲线
    g, states, n_unique = replay_global_order(dfs, hs_all)
    finals_diff = max(abs(states[-1, i] - dfs[i]["delta"].iloc[-1])
                      for i in range(len(dfs)))
    mean_line = states.mean(axis=1)
    p25, p75 = np.percentile(states, [25, 75], axis=1)
    lo, hi = states.min(axis=1), states.max(axis=1)

    fig, ax = plt.subplots(figsize=(10, 6.5))
    ax.fill_between(g, lo, hi, color="#4C72B0", alpha=0.12, lw=0,
                    label="Min-max across agents")
    ax.fill_between(g, p25, p75, color="#4C72B0", alpha=0.30, lw=0,
                    label="25th–75th percentiles across agents (P25–P75)")
    ax.plot(g, mean_line, color="#C44E52", lw=2.0, label="Mean across agents")
    ax.axhline(0, color="k", lw=0.6, alpha=0.5)
    ax.set_xlabel("Global transaction index")
    ax.set_ylabel(Y_LABEL)
    ax.margins(x=0.01)                    # 曲线贴近左右边框
    ax.ticklabel_format(style="sci", axis="y", scilimits=(0, 0))   # 纵轴科学计数法
    ax.legend(loc="upper left", framealpha=0.5)   # 半透明图例背景
    fig.tight_layout()
    save_fig(fig, fig_dir, "fig2_global_tx_order")
    plt.close(fig)

    print(f"全局交易序号: 去重后 {n_unique:,} 笔; "
          f"均值曲线最大绝对值 {np.abs(mean_line).max():.3e} (守恒校验); "
          f"期末与各文件末行 Δbalance 最大偏差 {finals_diff:.1f}")

    # ------------------------------------------------------------------ 图 3
    # 按相对进度归一化对齐: 每个 agent 的交易进度拉伸到 [0,1],
    # 所有 agent 在每个进度点都有取值(线性插值), 均值曲线终点即全体期末均值
    grid = np.linspace(0.0, 1.0, 201)
    curves = np.empty((len(dfs), len(grid)))
    for i, d in enumerate(dfs):
        xn = np.arange(len(d)) / (len(d) - 1)
        curves[i] = np.interp(grid, xn, d["delta"])
    mean_curve = curves.mean(axis=0)

    fig, ax = plt.subplots(figsize=(10, 6.5))
    ax.plot(grid, curves.T, color="grey", alpha=0.15, lw=0.7)
    ax.plot(grid, mean_curve, color="#C44E52", lw=2.2, label="Mean of all agents")
    ax.axhline(0, color="k", lw=0.6, alpha=0.5)
    ax.annotate("Mean balance change = 0", xy=(1.0, mean_curve[-1]),
                xytext=(-12, 16), textcoords="offset points",
                ha="right", color="#C44E52")
    ax.set_xlabel("Transaction progress of each agent")
    ax.xaxis.set_major_formatter(PercentFormatter(xmax=1.0))   # 0–1 显示为百分比
    ax.set_ylabel(Y_LABEL)
    ax.margins(x=0.01)                    # 曲线贴近左右边框
    ax.ticklabel_format(style="sci", axis="y", scilimits=(0, 0))   # 纵轴科学计数法
    ax.legend(loc="upper left")
    fig.tight_layout()
    fig.canvas.draw()                          # 先渲染以生成全部刻度标签
    ax.get_xticklabels()[-1].set_ha("right")   # 末刻度 "100%" 右端对齐刻度, 不越出边界
    tick_shift = ScaledTranslation(-5/72, 0, fig.dpi_scale_trans)   # 全部刻度左移 5 磅
    for lbl in ax.get_xticklabels():
        lbl.set_transform(lbl.get_transform() + tick_shift)
    save_fig(fig, fig_dir, "fig3_normalized_progress")
    plt.close(fig)

    # ------------------------------------------------------------------ 图 4
    # 每 5 个 agent 一组的余额轨迹, 覆盖全部 agent(展示在图册最后)
    group_cmap = plt.get_cmap("tab10")
    n_group_figs = 0
    for g_start in range(0, len(dfs), 5):
        group = dfs[g_start:g_start + 5]
        first_no = group[0]["agent"].iloc[0].replace("agent-", "")
        last_no = group[-1]["agent"].iloc[0].replace("agent-", "")
        fig, ax = plt.subplots(figsize=(10, 6.5))
        for i, d in enumerate(group):
            color = group_cmap(i)
            ax.plot(np.arange(len(d)), d["delta"], color=color, lw=1.1,
                    marker="o", ms=3, label=d["agent"].iloc[0])
        ax.set_xlabel(X_LABEL)
        ax.set_ylabel(Y_LABEL)
        ax.margins(x=0.01)                # 曲线贴近左右边框
        ax.ticklabel_format(style="sci", axis="y", scilimits=(0, 0))   # 纵轴科学计数法
        ax.legend(loc="upper right", ncol=2, bbox_to_anchor=(0.75, 1),
                  markerscale=2, columnspacing=0.6, labelspacing=0.35,
                  handletextpad=0.5)
        fig.tight_layout()
        save_fig(fig, fig_dir, f"fig4_agents_{first_no}-{last_no}")
        plt.close(fig)
        n_group_figs += 1

    print(f"已生成 fig1 总览图 + fig2 全局分布图 + fig3 归一化图 "
          f"+ {n_group_figs} 张 fig4 分组图(PNG + PDF)到 {fig_dir}")


if __name__ == "__main__":
    main()
