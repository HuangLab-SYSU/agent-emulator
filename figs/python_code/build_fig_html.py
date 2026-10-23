#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
将 figs_results 目录下 plot_computing_resources_trades.py 生成的算力资源交易 PNG 汇编成一个 HTML 图册页面。

页面为纯静态 HTML + 内联 CSS/JS, 图片使用相对路径引用, 通过 file:// 直接打开即可查看:
  - 每张 RWA 图独占一个编号章节(图 1..N), 标题放在图像外侧的章节标题栏,
    图内本身不含标题(绘图脚本已去掉标题);
  - 点击任意图片在当前页内弹层放大(灯箱), 点击右上角 ×、图片外区域或按 Esc 关闭;
    中键/Ctrl+点击仍可在新标签页打开原图
  - 页面文字(标题/章节/元信息/页脚)支持中英文切换, 右上角按钮或按 L 键切换,
    偏好通过 localStorage 记忆; 图内文字由画图脚本决定, 不受切换影响

用法:
  python3 build_fig_html.py                       # 默认扫描 figs/figs_results/
  python3 build_fig_html.py --fig-dir <目录> --rwa-dir <实验rwa目录>
"""

import argparse
import html
from datetime import datetime
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_FIG_DIR = REPO_ROOT / "figs" / "figs_results"

PAGE_TITLE_ZH = "AgentEmulator 实验 · 算力资源交易图表"
PAGE_TITLE_EN = "AgentEmulator Experiment · Computing Resources Trading Figures"

# 算力资源交易图章节: 按展示顺序排列(文件名前缀, 中文标题, 英文标题)。
# 展示顺序: 参与者 -> 资金流网络 -> 资产总览 -> 资产定价 -> 订单结构 -> 资金流。
# 编号按实际存在的图在页面上的出现顺序分配; 缺数据的图自动跳过。
FIG_SECTIONS = [
    ("computing_resources_agent_buy_sell_counts", "各 Agent 成交买入、挂卖与未成交买单次数",
     "Settled Buy, Listed Sell and Refused Buy Counts per Agent"),
    ("computing_resources_agent_net_value", "各 Agent 净交易额（卖出所得 − 买入支出）",
     "Net Trading Value per Agent (Earned − Spent)"),
    ("computing_resources_agent_network", "Agent 交易网络图（有向边线宽∝成交金额）",
     "Agent Trade Network (Directed Edges, Width ∝ Settled Value)"),
    ("computing_resources_value_by_compute", "各算力资源成交总金额",
     "Total Buy Value by Compute ID"),
    ("computing_resources_compute_market_share", "各算力资源按成交金额的市场份额",
     "Market Share by Buy Value across Compute IDs"),
    ("computing_resources_quantity_value_bubble", "各算力资源总数量-总金额-成交笔数气泡图",
     "Quantity-Value Bubble per Compute ID (Bubble Size = Trade Count)"),
    ("computing_resources_price_boxplot", "各算力资源成交单价分布箱线图",
     "Settled Unit Price Distribution per Compute ID"),
    ("computing_resources_quantity_boxplot", "各算力资源成交数量分布箱线图",
     "Settled Quantity Distribution per Compute ID"),
    ("computing_resources_price_quantity_scatter", "成交单价-数量散点图（点大小=成交金额）",
     "Settled Price-Quantity Scatter (Marker Size = Value)"),
    ("computing_resources_cumulative_value_timeline", "各算力资源累计成交金额曲线",
     "Cumulative Buy Value per Compute ID"),
    ("computing_resources_buyer_seller_heatmap", "买方-卖方成交金额热力图",
     "Buyer-Seller Settled Value Heatmap"),
]

CSS = """
body { font-family: -apple-system, "PingFang SC", "Hiragino Sans GB", sans-serif;
       margin: 0; background: #f5f6f8; color: #222; }
header { background: #1f2937; color: #fff; padding: 20px 28px;
         display: flex; align-items: center; justify-content: space-between;
         gap: 16px; }
header h1 { margin: 0 0 6px; font-size: 22px; }
header .meta { font-size: 13px; color: #b7bcc4; line-height: 1.7; }
.lang-btn { flex: none; background: #3b4657; color: #fff; border: 1px solid #5b6675;
            border-radius: 16px; padding: 8px 18px; font-size: 14px;
            cursor: pointer; user-select: none; }
.lang-btn:hover { background: #4a566a; }
main { max-width: 1280px; margin: 0 auto; padding: 20px 24px 48px; }
h2 { font-size: 17px; margin: 28px 0 12px; border-left: 4px solid #4C72B0;
     padding-left: 10px; }
a img { border: 0; }
.overview img { width: 100%; height: auto; border-radius: 6px;
                box-shadow: 0 1px 6px rgba(0,0,0,.12); }
footer { text-align: center; font-size: 12px; color: #999; padding-bottom: 24px; }
a.fig-link { cursor: zoom-in; }
#lightbox { position: fixed; inset: 0; background: rgba(15,18,24,.86); display: none;
            align-items: center; justify-content: center; z-index: 1000; cursor: zoom-out; }
#lightbox.open { display: flex; }
#lightbox img { max-width: 92vw; max-height: 88vh; border-radius: 4px;
                box-shadow: 0 4px 32px rgba(0,0,0,.45); cursor: default; }
#lightbox-close { position: absolute; top: 18px; right: 22px; width: 40px; height: 40px;
                  border-radius: 50%; border: 1px solid rgba(255,255,255,.35);
                  background: rgba(31,41,55,.7); color: #fff; font-size: 22px;
                  line-height: 1; cursor: pointer; }
#lightbox-close:hover { background: rgba(74,86,106,.9); }
"""

JS = """
(function () {
  var btn = document.getElementById('lang-toggle');
  var ZH = %s, EN = %s;
  function apply(lang) {
    var zh = lang === 'zh';
    document.querySelectorAll('[data-zh]').forEach(function (el) {
      el.textContent = zh ? el.getAttribute('data-zh') : el.getAttribute('data-en');
    });
    document.title = zh ? ZH : EN;
    document.documentElement.lang = zh ? 'zh-CN' : 'en';
    btn.textContent = zh ? 'EN' : '中文';
    try { localStorage.setItem('gallery-lang', lang); } catch (e) {}
  }
  function toggle() {
    apply(document.documentElement.lang === 'zh-CN' ? 'en' : 'zh');
  }
  btn.addEventListener('click', toggle);
  document.addEventListener('keydown', function (ev) {
    if ((ev.key === 'l' || ev.key === 'L') && !ev.metaKey && !ev.ctrlKey && !ev.altKey) toggle();
  });
  var saved = null;
  try { saved = localStorage.getItem('gallery-lang'); } catch (e) {}
  apply(saved === 'en' ? 'en' : 'zh');

  // 灯箱: 点击图片当前页放大, × / 图片外区域 / Esc 关闭。
  // 修饰键点击不拦截, 保留浏览器原生"在新标签页打开"。
  var box = document.getElementById('lightbox');
  var boxImg = document.getElementById('lightbox-img');
  function closeBox() { box.classList.remove('open'); }
  document.querySelectorAll('main a.fig-link').forEach(function (a) {
    a.addEventListener('click', function (ev) {
      if (ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
      ev.preventDefault();
      var inner = a.querySelector('img');
      boxImg.src = a.getAttribute('href');
      boxImg.alt = inner ? (inner.alt || '') : '';
      box.classList.add('open');
    });
  });
  document.getElementById('lightbox-close').addEventListener('click', closeBox);
  box.addEventListener('click', function (ev) {
    if (ev.target === box) closeBox();
  });
  document.addEventListener('keydown', function (ev) {
    if (ev.key === 'Escape') closeBox();
  });
})();
"""


def collect_sections(fig_dir: Path):
    """按 FIG_SECTIONS 顺序收集实际存在的 PNG, 返回 ([(序号, 路径, 中文, 英文)], 其他PNG)。"""
    pngs = sorted(fig_dir.glob("*.png"))
    known_prefixes = {stem for stem, _, _ in FIG_SECTIONS}
    sections = []
    used = set()
    for prefix, zh, en in FIG_SECTIONS:
        match = [p for p in pngs if p.name.startswith(prefix)]
        if match:
            sections.append((match[0], zh, en))
            used.add(match[0].name)
    others = [p for p in pngs if p.name not in used and not p.name.startswith(tuple(known_prefixes))]
    return sections, others


def h2(zh: str, en: str) -> str:
    return (f'<h2 data-zh="{html.escape(zh)}" data-en="{html.escape(en)}">'
            f'{html.escape(zh)}</h2>')


def overview_img(p: Path, alt: str) -> str:
    name = html.escape(p.name)
    return (f'<div class="overview"><a class="fig-link" href="{name}" target="_blank">'
            f'<img src="{name}" alt="{html.escape(alt)}"></a></div>')


def main():
    parser = argparse.ArgumentParser(description="Build the HTML gallery page for computing resources trading figures")
    parser.add_argument("--fig-dir", type=Path, default=DEFAULT_FIG_DIR,
                        help=f"Directory containing the PNGs; index.html is also written here (default: {DEFAULT_FIG_DIR})")
    parser.add_argument("--rwa-dir", type=Path, default=None,
                        help="Directory of experiment computing resources CSVs (only used to show the data source on the page)")
    parser.add_argument("--data-dir", type=Path, default=None,
                        help=argparse.SUPPRESS)  # 兼容旧调用参数, 仅忽略
    args = parser.parse_args()

    fig_dir = args.fig_dir
    sections, others = collect_sections(fig_dir)
    n_figs = len(sections) + len(others)
    if n_figs == 0:
        print(f"No PNG files found in {fig_dir}; run plot_computing_resources_trades.py first; skip gallery")
        return

    gen_time = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    meta_bits = [(f"生成时间：{gen_time}", f"Generated: {gen_time}"),
                 (f"图表数量：{n_figs} 张", f"Figures: {n_figs}")]
    if args.rwa_dir:
        src = str(args.rwa_dir)
        meta_bits.append((f"算力资源数据：{src}", f"Computing resources data: {src}"))
    meta_html = ' <span class="sep">|</span> '.join(
        f'<span data-zh="{html.escape(z)}" data-en="{html.escape(e)}">{html.escape(z)}</span>'
        for z, e in meta_bits)

    parts = [
        "<!DOCTYPE html>",
        '<html lang="zh-CN">',
        "<head>",
        '<meta charset="utf-8">',
        '<meta name="viewport" content="width=device-width, initial-scale=1">',
        f"<title>{html.escape(PAGE_TITLE_ZH)}</title>",
        f"<style>{CSS}</style>",
        "</head>",
        "<body>",
        "<header><div>",
        f'<h1 id="page-title" data-zh="{html.escape(PAGE_TITLE_ZH)}" '
        f'data-en="{html.escape(PAGE_TITLE_EN)}">{html.escape(PAGE_TITLE_ZH)}</h1>',
        f'<div class="meta">{meta_html}</div>',
        "</div>",
        '<button id="lang-toggle" class="lang-btn" type="button" '
        'title="切换中英文 / Toggle language (L)">EN</button>',
        "</header>",
        "<main>",
    ]

    for i, (p, zh, en) in enumerate(sections, start=1):
        parts.append(h2(f"图 {i} · {zh}", f"Fig. {i} · {en}"))
        parts.append(overview_img(p, zh))

    for p in others:  # 兜底: 展示任何其他命名的图
        parts.append(h2(p.stem, p.stem))
        parts.append(overview_img(p, p.stem))

    footer_zh = ("由 figs/python_code/build_fig_html.py 自动生成 · "
                 "点击任意图片可在当前页放大查看，点击图片外区域或右上角 × 关闭")
    footer_en = ("Auto-generated by figs/python_code/build_fig_html.py · "
                 "Click any figure to enlarge in place; click outside the image "
                 "or the × at the top right to close")
    parts.append("</main>")
    parts.append('<div id="lightbox">'
                 '<button id="lightbox-close" type="button" title="关闭 / Close (Esc)">×</button>'
                 '<img id="lightbox-img" alt=""></div>')
    parts.append(f'<footer data-zh="{html.escape(footer_zh)}" '
                 f'data-en="{html.escape(footer_en)}">{html.escape(footer_zh)}</footer>')
    parts.append("<script>" + JS % (repr(PAGE_TITLE_ZH), repr(PAGE_TITLE_EN)) + "</script>")
    parts.append("</body></html>")

    out = fig_dir / "index.html"
    out.write_text("\n".join(parts), encoding="utf-8")
    print(f"Generated HTML gallery: {out} ({n_figs} computing resources figures, titles placed outside the images)")


if __name__ == "__main__":
    main()
