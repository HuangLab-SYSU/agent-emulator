#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
将 figs_results 目录下 plot_agent_balance.py 生成的 PNG 汇编成一个 HTML 图册页面。

页面为纯静态 HTML + 内联 CSS/JS, 图片使用相对路径引用, 通过 file:// 直接打开即可查看:
  - 页面顺序: 总览 -> 全局交易顺序分布 -> 归一化进度 -> 分组轨迹网格,
    图 1–4 编号与 PNG 文件名前缀(fig1_/fig2_/fig3_/fig4_)一一对应
  - 点击任意图片在当前页内弹层放大(灯箱), 点击右上角 ×、图片外区域或按 Esc 关闭;
    中键/Ctrl+点击仍可在新标签页打开原图
  - 页面文字(标题/章节/元信息/页脚)支持中英文切换, 右上角按钮或按 L 键切换,
    偏好通过 localStorage 记忆; 图内文字由画图脚本决定, 不受切换影响

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

PAGE_TITLE_ZH = "AgentEmulator 实验 · Agent 余额生命周期图表"
PAGE_TITLE_EN = "AgentEmulator Experiment · Agent Balance Lifecycle Figures"

# 图 1: 排在最前(文件名前缀, 中文标题, 英文标题)
FIG1_ENTRY = ("fig1_all_agents_overview",
              "图 1 · 全部 Agent 总览",
              "Fig. 1 · Overview of All Agents")

# 分组轨迹网格(文件名前缀 fig4_*): 展示在页面最后, 编号为图 4
GROUPS_HEAD_ZH = "图 4 · Agent 余额变化的组图（每 5 个 Agent 一组）"
GROUPS_HEAD_EN = "Fig. 4 · Grouped Agent Balance Changes (5 Agents per Subfig)"

# 其余单幅图: 展示在图 1(总览)之后、分组网格之前, 按展示位置编号
TAIL_SINGLES = [
    ("fig2_global_tx_order",
     "图 2 · 全局交易序号下的 Agent 余额变化",
     "Fig. 2 · Agent Balance Changes over the Global Transaction Index"),
    ("fig3_normalized_progress",
     "图 3 · 按交易处理进度归一化对齐的余额变化",
     "Fig. 3 · The Change of Balance Aligned by Normalized Transaction Processing Progress"),
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
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
        gap: 16px; }
.card { background: #fff; border-radius: 6px; padding: 10px;
        box-shadow: 0 1px 4px rgba(0,0,0,.10); }
.card img { width: 100%; height: auto; display: block; }
.card .cap { font-size: 13px; color: #555; padding: 6px 2px 2px; text-align: center; }
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


def collect_pngs(fig_dir: Path):
    """收集 PNG: 返回 (fig1路径, 其余单幅图[(路径,中文,英文)...], fig4分组列表, 未知图列表)。"""
    pngs = sorted(fig_dir.glob("*.png"))
    fig1 = next((p for p in pngs if p.name.startswith(FIG1_ENTRY[0])), None)
    tail = []
    for prefix, zh, en in TAIL_SINGLES:
        match = [p for p in pngs if p.name.startswith(prefix)]
        if match:
            tail.append((match[0], zh, en))
    known = {FIG1_ENTRY[0]} | {p for p, _, _ in TAIL_SINGLES} | {"fig4"}
    groups = [p for p in pngs if p.name.startswith("fig4_")]
    others = [p for p in pngs if not any(p.name.startswith(k) for k in known)]
    return fig1, tail, groups, others


def group_caption(name: str) -> str:
    """fig4_agents_001-005.png -> Agent 001–005(语言无关)"""
    stem = name[:-4] if name.endswith(".png") else name
    if stem.startswith("fig4_agents_"):
        pair = stem[len("fig4_agents_"):]
        return f"Agent {pair.replace('-', '–')}"
    return stem


def h2(zh: str, en: str) -> str:
    return (f'<h2 data-zh="{html.escape(zh)}" data-en="{html.escape(en)}">'
            f'{html.escape(zh)}</h2>')


def overview_img(p: Path, alt: str) -> str:
    name = html.escape(p.name)
    return (f'<div class="overview"><a class="fig-link" href="{name}" target="_blank">'
            f'<img src="{name}" alt="{html.escape(alt)}"></a></div>')


def main():
    parser = argparse.ArgumentParser(description="生成实验图表 HTML 图册")
    parser.add_argument("--fig-dir", type=Path, default=DEFAULT_FIG_DIR,
                        help=f"PNG 所在目录, index.html 也生成在这里(默认 {DEFAULT_FIG_DIR})")
    parser.add_argument("--data-dir", type=Path, default=None,
                        help="实验 agent CSV 目录(仅用于在页面显示数据来源与 agent 数量)")
    args = parser.parse_args()

    fig_dir = args.fig_dir
    fig1, tail, groups, others = collect_pngs(fig_dir)
    n_figs = (1 if fig1 else 0) + len(tail) + len(groups) + len(others)
    if n_figs == 0:
        raise SystemExit(f"未在 {fig_dir} 找到任何 PNG, 请先运行 plot_agent_balance.py")

    n_agents = len(list(args.data_dir.glob("agent-*.csv"))) if args.data_dir else None
    gen_time = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    meta_bits = [(f"生成时间：{gen_time}", f"Generated: {gen_time}"),
                 (f"图表数量：{n_figs} 张", f"Figures: {n_figs}")]
    if args.data_dir:
        src = str(args.data_dir)
        meta_bits.append((f"数据来源：{src}（{n_agents} 个 agent）"
                          if n_agents else f"数据来源：{src}",
                          f"Data source: {src} ({n_agents} agents)"
                          if n_agents else f"Data source: {src}"))
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

    # 图 1
    if fig1:
        parts.append(h2(*FIG1_ENTRY[1:]))
        parts.append(overview_img(fig1, FIG1_ENTRY[1]))

    # 图 4 / 图 3 等其余单幅图
    for p, zh, en in tail:
        parts.append(h2(zh, en))
        parts.append(overview_img(p, zh))

    # 图 2 分组网格(语言无关的 Agent 编号作说明), 排在页面最后
    if groups:
        parts.append(h2(GROUPS_HEAD_ZH, GROUPS_HEAD_EN))
        parts.append('<div class="grid">')
        for p in groups:
            name = html.escape(p.name)
            cap = html.escape(group_caption(p.name))
            parts.append(f'<div class="card"><a class="fig-link" href="{name}" target="_blank">'
                         f'<img src="{name}" alt="{cap}" loading="lazy"></a>'
                         f'<div class="cap">{cap}</div></div>')
        parts.append("</div>")

    for p in others:  # 兜底: 展示任何其他命名的图
        parts.append(f"<h2>{html.escape(p.stem)}</h2>")
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
    print(f"已生成 HTML 图册: {out}（共 {n_figs} 张图, 编号按展示顺序: 总览=图1, 全局顺序=图2, 归一化=图3, 分组=图4）")


if __name__ == "__main__":
    main()
