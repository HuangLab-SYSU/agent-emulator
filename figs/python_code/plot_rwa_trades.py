#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Plot RWA buy/sell trading figures from AgentEmulator RWA CSV outputs.

Usage:
  python3 plot_rwa_trades.py --rwa-dir exp/agentemu-results/round_001/rwa --fig-dir figs/figs_results
"""

import argparse
from pathlib import Path

import matplotlib.pyplot as plt
import pandas as pd

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_RWA_DIR = REPO_ROOT / "exp" / "agentemu-results" / "round_001" / "rwa"
DEFAULT_FIG_DIR = REPO_ROOT / "figs" / "figs_results"


def load_orders(rwa_dir: Path) -> pd.DataFrame:
    path = rwa_dir / "rwa_orders.csv"
    if not path.exists():
        raise SystemExit(f"未找到 {path}")

    df = pd.read_csv(path, dtype={
        "value": str,
        "unit_price": str,
        "quantity": "Int64",
        "insufficient_balance": str,
    })
    if df.empty:
        raise SystemExit(f"{path} 没有 RWA 交易")

    df["value_int"] = df["value"].fillna("0").replace("", "0").map(int)
    df["unit_price_int"] = df["unit_price"].fillna("0").replace("", "0").map(int)
    df["quantity_int"] = df["quantity"].fillna(0).astype(int)
    df["insufficient_bool"] = df["insufficient_balance"].fillna("false").str.lower().eq("true")
    df["block_time_s"] = df["block_time_ms"].astype(float) / 1000.0

    return df


def rotate_x(deg: int = 25) -> None:
    plt.xticks(rotation=deg, ha="right")


def save_price_boxplot(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[(df["rwa_action"] == "buy") & (df["unit_price_int"] > 0)]
    if buys.empty:
        return

    groups = [(name, g["unit_price_int"].tolist()) for name, g in buys.groupby("compute_id", sort=True)]
    labels = [x[0] for x in groups]
    values = [x[1] for x in groups]

    plt.figure(figsize=(max(8, len(labels) * 1.4), 5))
    plt.boxplot(values, tick_labels=labels, showmeans=True)
    plt.title("RWA Buy Unit Price Distribution")
    plt.xlabel("compute_id")
    plt.ylabel("unit price")
    rotate_x()
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_price_boxplot.png", dpi=180)
    plt.close()


def save_quantity_boxplot(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"]
    if buys.empty:
        return

    groups = [(name, g["quantity_int"].tolist()) for name, g in buys.groupby("compute_id", sort=True)]
    labels = [x[0] for x in groups]
    values = [x[1] for x in groups]

    plt.figure(figsize=(max(8, len(labels) * 1.4), 5))
    plt.boxplot(values, tick_labels=labels, showmeans=True)
    plt.title("RWA Buy Quantity Distribution")
    plt.xlabel("compute_id")
    plt.ylabel("quantity")
    rotate_x()
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_quantity_boxplot.png", dpi=180)
    plt.close()


def save_value_by_compute(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"]
    if buys.empty:
        return

    agg = buys.groupby("compute_id", sort=True)["value_int"].sum()
    plt.figure(figsize=(max(8, len(agg) * 1.2), 5))
    agg.plot(kind="bar", color="#4C72B0")
    plt.title("RWA Total Buy Value by Compute ID")
    plt.xlabel("compute_id")
    plt.ylabel("total value")
    rotate_x()
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_value_by_compute.png", dpi=180)
    plt.close()


def save_agent_counts(df: pd.DataFrame, fig_dir: Path) -> None:
    if df.empty:
        return

    counts = df.groupby(["agent_id", "rwa_action"]).size().unstack(fill_value=0)
    for col in ["buy", "sell"]:
        if col not in counts.columns:
            counts[col] = 0
    counts = counts[["buy", "sell"]].sort_index()

    plt.figure(figsize=(max(8, len(counts) * 0.7), 5))
    counts.plot(kind="bar", ax=plt.gca(), color=["#55A868", "#C44E52"])
    plt.title("RWA Buy/Sell Counts by Agent")
    plt.xlabel("agent_id")
    plt.ylabel("count")
    rotate_x(35)
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_agent_buy_sell_counts.png", dpi=180)
    plt.close()


def save_insufficient_balance(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"]
    if buys.empty:
        return

    counts = buys.groupby("compute_id", sort=True)["insufficient_bool"].sum()
    if counts.sum() == 0:
        counts = pd.Series({"none": 0})

    plt.figure(figsize=(max(7, len(counts) * 1.2), 4.5))
    counts.plot(kind="bar", color="#DD8452")
    plt.title("RWA Buy Transactions with Insufficient Balance")
    plt.xlabel("compute_id")
    plt.ylabel("count")
    rotate_x()
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_insufficient_balance.png", dpi=180)
    plt.close()


def save_cumulative_value_timeline(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"].sort_values(["block_time_ms", "shard_id", "block_height", "tx_index"])
    if buys.empty:
        return

    plt.figure(figsize=(10, 5.5))
    for compute_id, group in buys.groupby("compute_id", sort=True):
        y = group["value_int"].cumsum()
        x = range(1, len(group) + 1)
        plt.step(x, y, where="post", marker="o", label=compute_id)
    plt.title("RWA Cumulative Buy Value by Compute ID")
    plt.xlabel("buy transaction index within compute_id")
    plt.ylabel("cumulative value")
    plt.legend(fontsize=9, ncol=2)
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_cumulative_value_timeline.png", dpi=180)
    plt.close()


def save_buyer_seller_heatmap(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"]
    if buys.empty:
        return

    pivot = buys.pivot_table(index="agent_id", columns="target_id", values="value_int", aggfunc="sum", fill_value=0)
    plt.figure(figsize=(max(8, len(pivot.columns) * 0.75), max(5.5, len(pivot.index) * 0.45)))
    plt.imshow(pivot.values, aspect="auto", cmap="YlGnBu")
    plt.colorbar(label="total buy value")
    plt.title("RWA Buyer-Seller Value Heatmap")
    plt.xlabel("seller target_id")
    plt.ylabel("buyer agent_id")
    plt.xticks(range(len(pivot.columns)), pivot.columns, rotation=35, ha="right")
    plt.yticks(range(len(pivot.index)), pivot.index)
    for i in range(pivot.shape[0]):
        for j in range(pivot.shape[1]):
            val = int(pivot.iat[i, j])
            if val > 0:
                plt.text(j, i, f"{val/1_000_000:.1f}M", ha="center", va="center", fontsize=7)
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_buyer_seller_heatmap.png", dpi=180)
    plt.close()


def save_price_quantity_scatter(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[(df["rwa_action"] == "buy") & (df["unit_price_int"] > 0)]
    if buys.empty:
        return

    compute_ids = sorted(buys["compute_id"].unique())
    cmap = plt.get_cmap("tab10")
    color_map = {cid: cmap(i % 10) for i, cid in enumerate(compute_ids)}
    plt.figure(figsize=(9, 5.5))
    for cid, group in buys.groupby("compute_id", sort=True):
        sizes = (group["value_int"] / max(float(buys["value_int"].max()), 1.0) * 500 + 60).astype(float)
        plt.scatter(group["quantity_int"], group["unit_price_int"], s=sizes, alpha=0.72, label=cid, color=color_map[cid], edgecolors="white", linewidth=0.6)
    plt.title("RWA Price-Quantity Scatter")
    plt.xlabel("buy quantity")
    plt.ylabel("unit price")
    plt.legend(fontsize=8, ncol=2)
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_price_quantity_scatter.png", dpi=180)
    plt.close()


def save_compute_market_share(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"]
    if buys.empty:
        return

    share = buys.groupby("compute_id", sort=True)["value_int"].sum().sort_values(ascending=False)
    plt.figure(figsize=(8, 6))
    plt.pie(share.values, labels=share.index, autopct="%1.1f%%", startangle=90, counterclock=False)
    plt.title("RWA Market Share by Buy Value")
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_compute_market_share.png", dpi=180)
    plt.close()


def save_agent_net_value(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"]
    if buys.empty:
        return

    spent = buys.groupby("agent_id")["value_int"].sum()
    earned = buys.groupby("target_id")["value_int"].sum()
    agents = sorted(set(spent.index) | set(earned.index))
    net = pd.Series({agent: int(earned.get(agent, 0)) - int(spent.get(agent, 0)) for agent in agents}).sort_values()
    colors = ["#C44E52" if v < 0 else "#55A868" for v in net.values]
    plt.figure(figsize=(max(9, len(net) * 0.65), 5.5))
    net.plot(kind="bar", color=colors)
    plt.axhline(0, color="black", linewidth=0.8)
    plt.title("RWA Agent Net Trading Value")
    plt.xlabel("agent_id")
    plt.ylabel("earned - spent")
    rotate_x(35)
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_agent_net_value.png", dpi=180)
    plt.close()


def save_quantity_value_bubble(df: pd.DataFrame, fig_dir: Path) -> None:
    buys = df[df["rwa_action"] == "buy"]
    if buys.empty:
        return

    agg = buys.groupby("compute_id", sort=True).agg(total_quantity=("quantity_int", "sum"), total_value=("value_int", "sum"), trade_count=("tx_hash", "count"))
    plt.figure(figsize=(8.5, 5.5))
    sizes = agg["trade_count"] * 180
    plt.scatter(agg["total_quantity"], agg["total_value"], s=sizes, alpha=0.72, color="#8172B2", edgecolors="white", linewidth=0.8)
    for cid, row in agg.iterrows():
        plt.annotate(cid, (row["total_quantity"], row["total_value"]), xytext=(6, 4), textcoords="offset points", fontsize=9)
    plt.title("RWA Quantity-Value Bubble by Compute ID")
    plt.xlabel("total buy quantity")
    plt.ylabel("total buy value")
    plt.tight_layout()
    plt.savefig(fig_dir / "rwa_quantity_value_bubble.png", dpi=180)
    plt.close()


def main() -> None:
    parser = argparse.ArgumentParser(description="绘制 RWA 买卖交易图表")
    parser.add_argument("--rwa-dir", type=Path, default=DEFAULT_RWA_DIR, help="RWA CSV 目录")
    parser.add_argument("--fig-dir", type=Path, default=DEFAULT_FIG_DIR, help="PNG 输出目录")
    args = parser.parse_args()

    df = load_orders(args.rwa_dir)
    args.fig_dir.mkdir(parents=True, exist_ok=True)

    save_price_boxplot(df, args.fig_dir)
    save_quantity_boxplot(df, args.fig_dir)
    save_value_by_compute(df, args.fig_dir)
    save_agent_counts(df, args.fig_dir)
    save_insufficient_balance(df, args.fig_dir)
    save_cumulative_value_timeline(df, args.fig_dir)
    save_buyer_seller_heatmap(df, args.fig_dir)
    save_price_quantity_scatter(df, args.fig_dir)
    save_compute_market_share(df, args.fig_dir)
    save_agent_net_value(df, args.fig_dir)
    save_quantity_value_bubble(df, args.fig_dir)

    print(f"RWA figures written to {args.fig_dir}")


if __name__ == "__main__":
    main()
