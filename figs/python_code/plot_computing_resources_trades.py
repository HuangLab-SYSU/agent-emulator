#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
绘制 RWA 买卖交易的系列图

数据: 每轮实验输出的 RWA 订单 CSV(exp/agentemu-results/round_*/computing_resources/computing_resources_orders.csv)
  - rwa_action:        buy / sell
  - agent_id:          买方(buy)或卖方(sell)的 agent 编号
  - target_id:         buy 的卖方(交易对手); sell 行为空
  - value:             转账金额(原始整数 wei, 成交买单 = unit_price x quantity,
                       卖单与被拒买单为 0, 超出 int64 需按字符串读入)
  - unit_price/quantity: 挂单单价与份数
  - rejected:          true = 购买时剩余库存不足, 编译为零值记录型交易(只留审计
                       痕迹, 不动余额不扣库存)
  - insufficient_balance: true = 回放账本时买方余额不足(同样未成交)

数据清洗:
  - 跨分片协议下同一笔交易会在两个分片各提交一次(相同 tx_hash), 统计前按
    tx_hash 去重(CSV 已按提交顺序排列, 保留首次提交), 否则所有计数/金额翻倍;
  - "成交买单" = buy 且 rejected=false 且 insufficient_balance=false,
    单价/数量/金额等成交统计只使用成交买单; 被拒/余额不足的买单不进入任何
    成交统计, 只以第三根柱子出现在各 Agent 买卖次数图中。

输出(figs/figs_results/, PNG 供 HTML 图册引用, PDF 供论文排版):
  - computing_resources_agent_buy_sell_counts     各 Agent 成交买入/挂卖/未成交买单次数
  - computing_resources_agent_net_value           各 Agent 净交易额(卖出所得 - 买入支出)
  - computing_resources_agent_network             Agent 交易网络图(有向边线宽∝成交金额)
  - computing_resources_value_by_compute          各算力资源成交总金额
  - computing_resources_compute_market_share      各算力资源成交额市场份额
  - computing_resources_quantity_value_bubble     各算力资源 总数量-总金额-成交笔数 气泡图
  - computing_resources_price_boxplot             各算力资源成交单价分布箱线图
  - computing_resources_quantity_boxplot          各算力资源成交数量分布箱线图
  - computing_resources_price_quantity_scatter    成交单价-数量散点(点大小=金额, 颜色=资源)
  - computing_resources_cumulative_value_timeline 各算力资源累计成交金额曲线
  - computing_resources_buyer_seller_heatmap      买方-卖方成交金额热力图

用法:
  python3 plot_rwa_trades.py
      # 不带参数: 自动选取 exp/agentemu-results/ 下编号最大一轮的 rwa/
  python3 plot_rwa_trades.py --rwa-dir <目录> --fig-dir <目录>
"""

import argparse
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.patches as mpatches
import matplotlib.pyplot as plt
from matplotlib.lines import Line2D
import numpy as np
import pandas as pd

# 仓库根目录 = 本脚本上三级(figs/python_code/ -> figs/ -> 仓库根)
REPO_ROOT = Path(__file__).resolve().parents[2]
RESULTS_ROOT = REPO_ROOT / "exp" / "agentemu-results"
DEFAULT_FIG_DIR = REPO_ROOT / "figs" / "figs_results"

COLOR_BLUE = "#4C72B0"
COLOR_GREEN = "#55A868"
COLOR_RED = "#C44E52"
COLOR_ORANGE = "#DD8452"


def latest_rwa_dir() -> Path:
    """取 exp/agentemu-results/ 下编号最大一轮(round_NNN 零填充, 字典序即轮次序)的 computing_resources/ 目录。"""
    if not RESULTS_ROOT.is_dir():
        raise SystemExit(f"Experiment results directory not found: {RESULTS_ROOT}")
    rwa_dirs = [r / "computing_resources" for r in sorted(RESULTS_ROOT.glob("round_*"))
                if (r / "computing_resources" / "computing_resources_orders.csv").is_file()]
    if not rwa_dirs:
        raise SystemExit(f"No round_* directory containing computing_resources/computing_resources_orders.csv under {RESULTS_ROOT}")
    return rwa_dirs[-1]


# ---------------------------------------------------------------------------
# 全局样式(与 plot_agent_balance.py 保持一致)
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
        print(f"Generated {out}")


def rotate_x(deg: int = 25):
    plt.setp(plt.gca().get_xticklabels(), rotation=deg, ha="right")


def sci_y(ax):
    """金额类纵轴用科学计数法(wei 原始单位量级大)。"""
    ax.ticklabel_format(style="sci", axis="y", scilimits=(0, 0))


# ---------------------------------------------------------------------------
# 数据加载与清洗
# ---------------------------------------------------------------------------
def load_orders(rwa_dir: Path) -> pd.DataFrame:
    path = rwa_dir / "computing_resources_orders.csv"
    if not path.exists():
        raise SystemExit(f"未找到 {path}")

    df = pd.read_csv(path, dtype={
        "value": str,
        "unit_price": str,
        "quantity": "Int64",
        "insufficient_balance": str,
        "rejected": str,
    })
    if df.empty:
        raise SystemExit(f"{path} 没有算力资源交易")

    df["value_int"] = df["value"].fillna("0").replace("", "0").map(int)
    df["unit_price_int"] = df["unit_price"].fillna("0").replace("", "0").map(int)
    df["quantity_int"] = df["quantity"].fillna(0).astype(int)
    df["rejected_bool"] = df["rejected"].fillna("false").str.lower().eq("true")
    df["insufficient_bool"] = df["insufficient_balance"].fillna("false").str.lower().eq("true")

    # 同一笔交易在两个分片各有一条记录(业务字段一致), 保留首次提交那条。
    # CSV 本身已按 (提交时间, 分片, 高度, 索引) 排序, keep="first" 即源分片记录。
    before = len(df)
    df = df.drop_duplicates(subset="tx_hash", keep="first").reset_index(drop=True)
    if len(df) < before:
        print(f"Deduplicated cross-shard records: {before} rows -> {len(df)} unique transactions")

    return df


def settled_buys(df: pd.DataFrame) -> pd.DataFrame:
    """真正成交(扣减库存并转账)的买单。"""
    return df[(df["rwa_action"] == "buy")
              & ~df["rejected_bool"] & ~df["insufficient_bool"]]


def refused_buys(df: pd.DataFrame) -> pd.DataFrame:
    """未成交买单: 库存不足被拒(rejected)或买方余额不足(insufficient)。"""
    return df[(df["rwa_action"] == "buy")
              & (df["rejected_bool"] | df["insufficient_bool"])]


def fmt_amount(v: float) -> str:
    """热力图单元格标注: wei 原始值缩写。"""
    if v >= 1_000_000:
        return f"{v / 1_000_000:.1f}M"
    if v >= 1_000:
        return f"{v / 1_000:.0f}K"
    return f"{int(v)}"


# ---------------------------------------------------------------------------
# 图: Agent 维度
# ---------------------------------------------------------------------------
def save_agent_counts(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各 Agent 的成交买入次数、挂卖次数与未成交买单次数(三色分组柱状图)。"""
    counts = pd.DataFrame({
        "buy": settled_buys(df).groupby("agent_id").size(),
        "sell": df[df["rwa_action"] == "sell"].groupby("agent_id").size(),
        "refused": refused_buys(df).groupby("agent_id").size(),
    }).fillna(0).astype(int).sort_index()
    if counts.empty:
        return None

    fig, ax = plt.subplots(figsize=(max(10, len(counts) * 0.9), 6.5))
    x = np.arange(len(counts))
    width = 0.25
    ax.bar(x - width, counts["buy"], width=width, color=COLOR_GREEN, label="Settled buys")
    ax.bar(x, counts["sell"], width=width, color=COLOR_RED, label="Listed sells")
    ax.bar(x + width, counts["refused"], width=width, color=COLOR_ORANGE, label="Refused buys")
    # 显式限定 x 轴范围: 每个类目占 1 个单位宽度, 类目极少时柱子也不会撑满整图。
    ax.set_xlim(-0.75, len(counts) - 0.25)
    ax.set_xticks(x)
    ax.set_xticklabels(counts.index)
    ax.set_xlabel("Agent ID")
    ax.set_ylabel("Count")
    ax.set_ylim(0, max(float(counts.max().max()) * 1.15, 1))
    ax.legend(loc="upper right", framealpha=0.5)
    rotate_x(35)
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_agent_buy_sell_counts")
    plt.close(fig)
    return "computing_resources_agent_buy_sell_counts"


def save_agent_net_value(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各 Agent 净交易额(作为卖方收到的买入金额 - 自己买入支出), 正=净卖方。"""
    buys = settled_buys(df)
    spent = buys.groupby("agent_id")["value_int"].sum()
    earned = buys.groupby("target_id")["value_int"].sum()
    agents = sorted(set(spent.index) | set(earned.index))
    if not agents:
        return None
    net = pd.Series({a: int(earned.get(a, 0)) - int(spent.get(a, 0)) for a in agents}).sort_values()

    fig, ax = plt.subplots(figsize=(max(10, len(net) * 0.9), 6.5))
    ax.bar(np.arange(len(net)), net.values,
           color=np.where(net.values >= 0, COLOR_GREEN, COLOR_RED))
    ax.set_xlim(-0.75, len(net) - 0.25)
    ax.set_xticks(np.arange(len(net)))
    ax.set_xticklabels(net.index)
    ax.axhline(0, color="k", lw=0.8)
    ax.set_xlabel("Agent ID")
    ax.set_ylabel("Net trading value (wei)")
    pad = (float(net.max()) - float(net.min())) * 0.08 or 1.0
    ax.set_ylim(float(net.min()) - pad, float(net.max()) + pad)
    sci_y(ax)
    rotate_x(35)
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_agent_net_value")
    plt.close(fig)
    return "computing_resources_agent_net_value"


# ---------------------------------------------------------------------------
# 图: 算力资源(compute_id)维度
# ---------------------------------------------------------------------------
def save_value_by_compute(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各算力资源的成交总金额。"""
    agg = settled_buys(df).groupby("compute_id", sort=True)["value_int"].sum()
    if agg.empty:
        return None

    fig, ax = plt.subplots(figsize=(max(10, len(agg) * 1.6), 6.5))
    ax.bar(np.arange(len(agg)), agg.values, color=COLOR_BLUE)
    ax.set_xticks(np.arange(len(agg)))
    ax.set_xticklabels(agg.index)
    ax.set_xlabel("Compute ID")
    ax.set_ylabel("Total buy value (wei)")
    ax.set_xlim(-0.75, len(agg) - 0.25)
    sci_y(ax)
    rotate_x()
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_value_by_compute")
    plt.close(fig)
    return "computing_resources_value_by_compute"


def save_compute_market_share(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各算力资源按成交金额的市场份额(饼图)。

    小扇区挤在一起时楔外标签必然重叠, 因此图内只保留占比 >=4% 扇区的
    百分比标注, 资源名称与占比统一放到右侧图例。
    """
    share = settled_buys(df).groupby("compute_id", sort=True)["value_int"].sum().sort_values(ascending=False)
    if share.empty:
        return None

    fig, ax = plt.subplots(figsize=(11, 6.5))
    ax.grid(False)
    wedges, _, autotexts = ax.pie(
        share.values,
        autopct=lambda p: f"{p:.1f}%" if p >= 4 else "",
        startangle=90, counterclock=False,
        wedgeprops={"edgecolor": "white", "linewidth": 1.2},
        textprops={"fontsize": 20})
    for t in autotexts:   # tab10 深色扇区上用白字
        t.set_color("white")
    total = float(share.sum())
    ax.legend(wedges, [f"{cid} ({v / total:.1%})" for cid, v in share.items()],
              loc="center left", bbox_to_anchor=(1.02, 0.5),
              fontsize=17, framealpha=0.5)
    ax.set_aspect("equal")
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_compute_market_share")
    plt.close(fig)
    return "computing_resources_compute_market_share"


def save_quantity_value_bubble(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各算力资源 总成交数量-总成交金额 气泡图, 气泡大小=成交笔数。

    气泡到原点的斜率即该资源的平均成交单价, 用于横向对比资产定位。
    资源名称用图例(颜色)区分: 多个小数量资源会挤在原点附近,
    图上直接标注文字必然互相压盖。
    """
    agg = settled_buys(df).groupby("compute_id", sort=True).agg(
        total_quantity=("quantity_int", "sum"),
        total_value=("value_int", "sum"),
        trade_count=("tx_hash", "count"),
    )
    if agg.empty:
        return None

    cmap = plt.colormaps["tab10"]
    fig, ax = plt.subplots(figsize=(10, 6.5))
    for i, (cid, row) in enumerate(agg.iterrows()):
        ax.scatter(row["total_quantity"], row["total_value"],
                   s=row["trade_count"] * 180, alpha=0.72,
                   color=cmap(i % 10), edgecolors="white", linewidth=0.8,
                   label=cid)
    ax.set_xlabel("Total buy quantity (units)")
    ax.set_ylabel("Total buy value (wei)")
    ax.margins(x=0.08, y=0.1)
    sci_y(ax)
    ax.legend(fontsize=17, ncol=2, loc="upper left", framealpha=0.5)
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_quantity_value_bubble")
    plt.close(fig)
    return "computing_resources_quantity_value_bubble"


def save_price_boxplot(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各算力资源成交单价分布箱线图(均值用三角标出)。"""
    buys = settled_buys(df)
    buys = buys[buys["unit_price_int"] > 0]
    if buys.empty:
        return None

    groups = [(name, g["unit_price_int"].tolist()) for name, g in buys.groupby("compute_id", sort=True)]
    labels = [x[0] for x in groups]
    values = [x[1] for x in groups]

    fig, ax = plt.subplots(figsize=(max(10, len(labels) * 1.8), 6.5))
    ax.boxplot(values, tick_labels=labels, showmeans=True)
    ax.set_xlabel("Compute ID")
    ax.set_ylabel("Settled unit price (wei)")
    sci_y(ax)
    rotate_x()
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_price_boxplot")
    plt.close(fig)
    return "computing_resources_price_boxplot"


def save_quantity_boxplot(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各算力资源成交数量分布箱线图。"""
    buys = settled_buys(df)
    if buys.empty:
        return None

    groups = [(name, g["quantity_int"].tolist()) for name, g in buys.groupby("compute_id", sort=True)]
    labels = [x[0] for x in groups]
    values = [x[1] for x in groups]

    fig, ax = plt.subplots(figsize=(max(10, len(labels) * 1.8), 6.5))
    ax.boxplot(values, tick_labels=labels, showmeans=True)
    ax.set_xlabel("Compute ID")
    ax.set_ylabel("Settled quantity per buy (units)")
    rotate_x()
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_quantity_boxplot")
    plt.close(fig)
    return "computing_resources_quantity_boxplot"


def save_price_quantity_scatter(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """单笔成交的 单价-数量 散点: 颜色区分算力资源, 点大小=成交金额。"""
    buys = settled_buys(df)
    buys = buys[buys["unit_price_int"] > 0]
    if buys.empty:
        return None

    compute_ids = sorted(buys["compute_id"].unique())
    cmap = plt.colormaps["tab10"]
    color_map = {cid: cmap(i % 10) for i, cid in enumerate(compute_ids)}
    fig, ax = plt.subplots(figsize=(10, 6.5))
    for cid, group in buys.groupby("compute_id", sort=True):
        sizes = (group["value_int"] / max(float(buys["value_int"].max()), 1.0) * 500 + 60).astype(float)
        ax.scatter(group["quantity_int"], group["unit_price_int"], s=sizes, alpha=0.72,
                   label=cid, color=color_map[cid], edgecolors="white", linewidth=0.6)
    ax.set_xlabel("Quantity per buy (units)")
    ax.set_ylabel("Settled unit price (wei)")
    sci_y(ax)
    ax.legend(fontsize=17, ncol=2, framealpha=0.5)
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_price_quantity_scatter")
    plt.close(fig)
    return "computing_resources_price_quantity_scatter"


def save_cumulative_value_timeline(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """各算力资源按成交顺序的累计成交金额(阶梯图), 反映资金吸收节奏。"""
    buys = settled_buys(df).sort_values(["block_time_ms", "shard_id", "block_height", "tx_index"])
    if buys.empty:
        return None

    cmap = plt.colormaps["tab10"]
    fig, ax = plt.subplots(figsize=(10, 6.5))
    for i, (compute_id, group) in enumerate(buys.groupby("compute_id", sort=True)):
        y = group["value_int"].cumsum()
        x = range(1, len(group) + 1)
        ax.step(x, y, where="post", marker="o", ms=5, lw=1.4,
                label=compute_id, color=cmap(i % 10))
    ax.set_xlabel("Buy transaction index within compute ID")
    ax.set_ylabel("Cumulative buy value (wei)")
    sci_y(ax)
    ax.legend(fontsize=17, ncol=2, framealpha=0.5)
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_cumulative_value_timeline")
    plt.close(fig)
    return "computing_resources_cumulative_value_timeline"


def save_buyer_seller_heatmap(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """买方-卖方成交金额热力图: 谁向谁买了多少, 展示链上资金流向网络。"""
    buys = settled_buys(df)
    if buys.empty:
        return None

    pivot = buys.pivot_table(index="agent_id", columns="target_id",
                             values="value_int", aggfunc="sum", fill_value=0)
    fig, ax = plt.subplots(figsize=(max(10, len(pivot.columns) * 0.95),
                                    max(6.5, len(pivot.index) * 0.55)))
    im = ax.imshow(pivot.values, aspect="auto", cmap="YlGnBu")
    cbar = fig.colorbar(im, ax=ax)
    cbar.set_label("Total buy value (wei)", fontsize=22)
    ax.set_xticks(range(len(pivot.columns)), pivot.columns, rotation=35, ha="right")
    ax.set_yticks(range(len(pivot.index)), pivot.index)
    ax.set_xlabel("Seller (target) Agent ID")
    ax.set_ylabel("Buyer Agent ID")
    ax.grid(False)
    vmax = float(pivot.values.max())
    for i in range(pivot.shape[0]):
        for j in range(pivot.shape[1]):
            val = int(pivot.iat[i, j])
            if val > 0:
                dark = val > vmax * 0.55
                ax.text(j, i, fmt_amount(val), ha="center", va="center",
                        fontsize=13, color="white" if dark else "black")
    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_buyer_seller_heatmap")
    plt.close(fig)
    return "computing_resources_buyer_seller_heatmap"


# ---------------------------------------------------------------------------
# 图: Agent 交易网络
# ---------------------------------------------------------------------------
def save_agent_network(df: pd.DataFrame, fig_dir: Path) -> str | None:
    """Agent 交易网络图: 节点=Agent(环形布局), 有向边=买方→卖方。

    每条有向边代表一对(买方, 卖方)之间的全部成交(多笔合并为一条), 线宽与
    箭头大小按成交金额分成三档, 档位区间与右下图例严格对应; 金额最大的
    前 5 条边额外标注缩写金额; 卖单只是挂单、无资金流动, 不生成边。
    节点大小∝交易总规模(买入支出+卖出所得), 颜色=净方向:
    绿=净卖方, 红=净买方, 灰=持平或无成交。
    """
    buys = settled_buys(df)
    if buys.empty:
        return None

    flows = buys.groupby(["agent_id", "target_id"])["value_int"].sum()
    participants = (set(df["agent_id"].dropna()) | set(buys["agent_id"])
                    | set(buys["target_id"]))
    agents = sorted(participants)
    # 环形布局: 从正上方开始逆时针排布, 确定性优于力导向布局。
    angles = np.pi / 2 + np.linspace(0.0, 2.0 * np.pi, len(agents), endpoint=False)
    pos = {a: (float(np.cos(t)), float(np.sin(t))) for a, t in zip(agents, angles)}

    spent = buys.groupby("agent_id")["value_int"].sum()
    earned = buys.groupby("target_id")["value_int"].sum()
    volume = pd.Series({a: float(spent.get(a, 0)) + float(earned.get(a, 0)) for a in agents})
    net = pd.Series({a: float(earned.get(a, 0)) - float(spent.get(a, 0)) for a in agents})
    vmax_vol = float(volume.max()) or 1.0
    vmax_flow = float(flows.max()) or 1.0
    node_sizes = pd.Series({a: 350.0 + 2600.0 * float(volume[a]) / vmax_vol for a in agents})

    fig, ax = plt.subplots(figsize=(11.5, 9.5))
    ax.set_axis_off()
    ax.set_aspect("equal")

    # 边: 买方→卖方, 统一弧线; 互有往来的两条边弧向相反, 自然分开不重叠。
    # 线宽/箭头按成交金额分三档(按最大成交金额三等分), 区间与图例严格对应。
    rad = 0.16
    level_cut1, level_cut2 = vmax_flow / 3.0, 2.0 * vmax_flow / 3.0
    level_lw = (1.1, 2.8, 4.8)
    level_arrow = (11.0, 18.0, 26.0)

    def flow_level(v: float) -> int:
        return 0 if v <= level_cut1 else (1 if v <= level_cut2 else 2)

    for (buyer, seller), v in flows.items():
        lvl = flow_level(float(v))
        ax.add_patch(mpatches.FancyArrowPatch(
            pos[buyer], pos[seller],
            connectionstyle=f"arc3,rad={rad}",
            arrowstyle="-|>",
            mutation_scale=level_arrow[lvl],
            lw=level_lw[lvl],
            color=COLOR_BLUE, alpha=0.6,
            shrinkA=float(np.sqrt(node_sizes[buyer] / np.pi)) + 3.0,
            shrinkB=float(np.sqrt(node_sizes[seller] / np.pi)) + 3.0,
            zorder=1))
    # 金额最大的前 5 条边标注缩写金额, 其余靠线宽粗略表达, 避免文字拥挤。
    # 标注点若落入某节点的圆形范围, 沿径向推出到节点外侧, 避免被节点遮挡。
    clearance = 0.18
    for (buyer, seller), v in flows.sort_values(ascending=False).head(5).items():
        (x1, y1), (x2, y2) = pos[buyer], pos[seller]
        dist = float(np.hypot(x2 - x1, y2 - y1))
        mx, my = (x1 + x2) / 2.0, (y1 + y2) / 2.0
        nx, ny = -(y2 - y1) / dist, (x2 - x1) / dist   # 弧线凸出方向的法向量
        k = rad / 2.0
        tx, ty = mx + k * nx * dist, my + k * ny * dist
        for _ in range(2):
            nearest = min(agents, key=lambda a: (tx - pos[a][0]) ** 2 + (ty - pos[a][1]) ** 2)
            dx, dy = tx - pos[nearest][0], ty - pos[nearest][1]
            d = float(np.hypot(dx, dy))
            if d >= clearance:
                break
            scale = clearance / (d or 1e-6)
            tx = pos[nearest][0] + dx * scale
            ty = pos[nearest][1] + dy * scale
        ax.text(tx, ty, fmt_amount(float(v)),
                ha="center", va="center", fontsize=14, color="#1f3b5e", zorder=2)

    # 节点: 大小=交易总规模, 颜色=净方向。
    node_colors = [COLOR_GREEN if net[a] > 0 else (COLOR_RED if net[a] < 0 else "#8c8c8c")
                   for a in agents]
    ax.scatter([pos[a][0] for a in agents], [pos[a][1] for a in agents],
               s=node_sizes.loc[agents], c=node_colors,
               edgecolors="white", linewidths=1.5, zorder=3)
    for a in agents:
        x, y = pos[a]
        # 标注放在节点外侧: 左侧节点标注朝左、右侧朝右、正上/正下朝外,
        # 通过水平/垂直对齐让文字背离圆心伸展, 避免与节点或相邻标注重叠。
        lx, ly = x * 1.14, y * 1.14
        ha = "right" if x < -0.35 else ("left" if x > 0.35 else "center")
        va = "bottom" if y > 0.9 else ("top" if y < -0.9 else "center")
        ax.text(lx, ly, a, ha=ha, va=va, fontsize=17)

    ax.set_xlim(-1.5, 1.5)
    ax.set_ylim(-1.45, 1.5)

    # 图例: 左下=节点颜色含义, 右下=线宽与金额的对应(取最大流量的比例样本)。
    node_handles = [
        Line2D([], [], marker="o", ls="", color=COLOR_GREEN, markersize=13,
               label="Net seller (earned > spent)"),
        Line2D([], [], marker="o", ls="", color=COLOR_RED, markersize=13,
               label="Net buyer (spent > earned)"),
        Line2D([], [], marker="o", ls="", color="#8c8c8c", markersize=13,
               label="Neutral"),
    ]
    leg = ax.legend(handles=node_handles, loc="lower left", fontsize=16, framealpha=0.5)
    ax.add_artist(leg)
    flow_handles = [
        Line2D([], [], color=COLOR_BLUE, lw=level_lw[lvl], alpha=0.7,
               label=label)
        for lvl, label in enumerate((
            f"Flow ≤ {fmt_amount(level_cut1)}",
            f"{fmt_amount(level_cut1)} < Flow ≤ {fmt_amount(level_cut2)}",
            f"Flow > {fmt_amount(level_cut2)}"))
    ]
    ax.legend(handles=flow_handles, loc="lower right", fontsize=16, framealpha=0.5)

    fig.tight_layout()
    save_fig(fig, fig_dir, "computing_resources_agent_network")
    plt.close(fig)
    return "computing_resources_agent_network"


def main() -> None:
    parser = argparse.ArgumentParser(description="Plot the computing resources buy/sell trading figure series")
    parser.add_argument("--rwa-dir", type=Path, default=None,
                        help="Directory containing computing_resources_orders.csv (default: latest experiment round)")
    parser.add_argument("--fig-dir", type=Path, default=DEFAULT_FIG_DIR,
                        help=f"Output directory for figures (default: {DEFAULT_FIG_DIR})")
    args = parser.parse_args()

    rwa_dir = args.rwa_dir or latest_rwa_dir()
    fig_dir = args.fig_dir
    fig_dir.mkdir(parents=True, exist_ok=True)
    apply_style()

    df = load_orders(rwa_dir)
    buys = settled_buys(df)
    print(f"Loaded {len(df)} unique computing resources transactions from {rwa_dir} "
          f"({len(buys)} settled buys, {len(refused_buys(df))} refused buys, "
          f"{int((df['rwa_action'] == 'sell').sum())} sells)")

    generated = []
    for saver in (save_agent_counts,
                  save_agent_net_value,
                  save_agent_network,
                  save_value_by_compute,
                  save_compute_market_share,
                  save_quantity_value_bubble,
                  save_price_boxplot,
                  save_quantity_boxplot,
                  save_price_quantity_scatter,
                  save_cumulative_value_timeline,
                  save_buyer_seller_heatmap):
        stem = saver(df, fig_dir)
        if stem:
            generated.append(stem)

    if not generated:
        print("No figures generated: no settled or refused computing resources trades in the data")
        return
    print(f"Generated {len(generated)} computing resources figures (PNG + PDF) in {fig_dir}")


if __name__ == "__main__":
    main()
