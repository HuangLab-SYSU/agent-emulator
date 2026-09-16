#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
将 figs_results 目录下 plot_agent_balance.py 生成的 PNG 汇编成一个 HTML 图册页面。

页面为纯静态 HTML + 内联 CSS, 图片使用相对路径引用, 通过 file:// 直接打开即可查看:
  - 图 1(全部 agent 总览)、图 3(按交易进度归一化对齐)、图 4(按全局交易顺序统计)
    整幅展示
  - 图 2(每 5 个 agent 一组的余额轨迹)以缩略图网格展示, 点击在新标签页打开原图

用法:
  python3 build_fig_html.py                       # 默认扫描 figs/figs_results/
  python3 build_fig_html.py --fig-dir <目录> --data-dir <实验agents目录>
"""

import argparse
import html
from datetime import datetime
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_FIG_DIR = REPO_ROOT / "figs" / "figs_results"

PAGE_TITLE = "AgentEmulator 实验 · Agent 余额生命周期图表"

CSS = """
body { font-family: -apple-system, "PingFang SC", "Hiragino Sans GB", sans-serif;
       margin: 0; background: #f5f6f8; color: #222; }
header { background: #1f2937; color: #fff; padding: 20px 28px; }
header h1 { margin: 0 0 6px; font-size: 22px; }
header .meta { font-size: 13px; color: #b7bcc4; line-height: 1.7; }
main { max-width: 1280px; margin: 0 auto; padding: 20px 24px 48px; }
h2 { font-size: 17px; margin: 28px 0 12px; border-left: 4px solid #4C72B0;
     padding-left: 10px; }
a img { border: 0; }
.overview img { width: 100%; height: auto; border-radius: 6px;
                box-shadow: 0 1px 6px rgba(0,0,0,.12); }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
        gap: 16px; }
.card { background: #fff; border-radius: 6px; padding: 10px;
        box-shadow: 0 1px 4px rgba(0,0,0,.10); }
.card img { width: 100%; height: auto; display: block; }
.card .cap { font-size: 13px; color: #555; padding: 6px 2px 2px; text-align: center; }
footer { text-align: center; font-size: 12px; color: #999; padding-bottom: 24px; }
"""


# 单幅展示的图(按页面出现顺序排列), 文件名前缀 -> 中文标题
SINGLE_FIGS = [
    ("fig1_all_agents_overview", "图 1 · 全部 Agent 总览"),
    ("fig3_normalized_progress", "图 3 · 按交易进度归一化对齐的余额变化"),
    ("fig4_global_tx_order", "图 4 · 按全局交易顺序统计的全体 Agent 余额分布"),
]


def collect_pngs(fig_dir: Path):
    """收集 PNG: 单幅图(图1/图3/图4)按页面顺序在前, 图2分组按文件名排序
    (编号零填充, 字典序即数值序), 其余未知命名的图归入 others 兜底展示。"""
    pngs = sorted(fig_dir.glob("*.png"))
    known = {prefix for prefix, _ in SINGLE_FIGS} | {"fig2"}
    singles = []
    for prefix, caption in SINGLE_FIGS:
        match = [p for p in pngs if p.name.startswith(prefix)]
        if match:
            singles.append((match[0], caption))
    groups = [p for p in pngs if p.name.startswith("fig2_")]
    others = [p for p in pngs if not any(p.name.startswith(k) for k in known)]
    return singles, groups, others


def fig2_caption(name: str) -> str:
    """fig2_agents_001-005.png -> Agent 001–005"""
    stem = name[:-4] if name.endswith(".png") else name
    if stem.startswith("fig2_agents_"):
        pair = stem[len("fig2_agents_"):]
        return f"Agent {pair.replace('-', '–')}"
    return stem


def main():
    parser = argparse.ArgumentParser(description="生成实验图表 HTML 图册")
    parser.add_argument("--fig-dir", type=Path, default=DEFAULT_FIG_DIR,
                        help=f"PNG 所在目录, index.html 也生成在这里(默认 {DEFAULT_FIG_DIR})")
    parser.add_argument("--data-dir", type=Path, default=None,
                        help="实验 agent CSV 目录(仅用于在页面显示数据来源与 agent 数量)")
    args = parser.parse_args()

    fig_dir = args.fig_dir
    singles, groups, others = collect_pngs(fig_dir)
    if not singles and not groups and not others:
        raise SystemExit(f"未在 {fig_dir} 找到任何 PNG, 请先运行 plot_agent_balance.py")

    n_agents = len(list(args.data_dir.glob("agent-*.csv"))) if args.data_dir else None
    gen_time = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    meta_bits = [f"生成时间：{gen_time}",
                 f"图表数量：{len(singles) + len(groups) + len(others)} 张"]
    if args.data_dir:
        src = html.escape(str(args.data_dir))
        meta_bits.append(f"数据来源：{src}" + (f"（{n_agents} 个 agent）" if n_agents else ""))

    parts = [
        "<!DOCTYPE html>",
        '<html lang="zh-CN">',
        "<head>",
        '<meta charset="utf-8">',
        '<meta name="viewport" content="width=device-width, initial-scale=1">',
        f"<title>{html.escape(PAGE_TITLE)}</title>",
        f"<style>{CSS}</style>",
        "</head>",
        "<body>",
        f"<header><h1>{html.escape(PAGE_TITLE)}</h1>",
        f'<div class="meta">{" &nbsp;|&nbsp; ".join(meta_bits)}</div></header>',
        "<main>",
    ]

    for p, caption in singles:
        name = html.escape(p.name)
        cap = html.escape(caption)
        parts.append(f'<h2>{cap}</h2>')
        parts.append(f'<div class="overview"><a href="{name}" target="_blank">'
                     f'<img src="{name}" alt="{cap}"></a></div>')

    if groups:
        parts.append("<h2>图 2 · 分组余额轨迹（每 5 个 Agent 一组）</h2>")
        parts.append('<div class="grid">')
        for p in groups:
            name = html.escape(p.name)
            cap = html.escape(fig2_caption(p.name))
            parts.append(f'<div class="card"><a href="{name}" target="_blank">'
                         f'<img src="{name}" alt="{cap}" loading="lazy"></a>'
                         f'<div class="cap">{cap}</div></div>')
        parts.append("</div>")

    for p in others:  # 兜底: 展示任何其他命名的图
        name = html.escape(p.name)
        parts.append(f'<h2>{html.escape(p.stem)}</h2>')
        parts.append(f'<div class="overview"><a href="{name}" target="_blank">'
                     f'<img src="{name}" alt="{name}"></a></div>')

    parts.append("</main>")
    parts.append("<footer>由 figs/python_code/build_fig_html.py 自动生成 · "
                 "点击任意图片可在新标签页查看原图</footer>")
    parts.append("</body></html>")

    out = fig_dir / "index.html"
    out.write_text("\n".join(parts), encoding="utf-8")
    print(f"已生成 HTML 图册: {out}（共 {len(singles) + len(groups) + len(others)} 张图）")


if __name__ == "__main__":
    main()
