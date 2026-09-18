# AgentEmulator 用户使用指南

AgentEmulator 是构建在 BlockEmulator-X 之上的 **Trace 驱动的 Agent 行为仿真器**：用户使用 JSONL 文件描述一段 Agent 行为（加入、转账、退出），AgentEmulator 会将该 Trace 文件编译成真正的区块链交易，然后 **AgentEmulator** 自动启动一个私有的 BlockEmulator-X 集群把交易全部上链，最后留下完整的计划、测量数据，并**自动绘制 Agent 余额变化图集、生成可中英文切换的 HTML 图册页面**，在浏览器中直接展示。

```
trace 文件（join / pay / leave / 原始转账行）
        │  agentemu 编译（确定性 DID 派生）
        ▼
交易计划 agent_transactions.jsonl
        │  ChainRunner 自动构建二进制、派生配置、拉起集群（4 分片 × 4 节点）
        ▼
BlockEmulator-X 集群（plan_source 注入 → 共识 → 上链 → 自动收尾）
        ▼
计划 / 意图映射 / 注册表 / 每 Agent 账本 CSV / 链上测量 / 轮次汇总
        ▼
自动绘图（figs/python_code）→ figs/figs_results/ 下的 PNG + HTML 图册
        ▼
浏览器自动打开图册（支持中英文切换）
```

---

## 1. 环境准备

支持 macOS / Linux / Windows 三种系统，启动命令见第 2 节，行为完全一致。

| 依赖 | 版本要求 | 验证命令 | 用途 |
|---|---|---|---|
| Go | ≥ 1.25 | `go version` | 编译运行仿真器与集群 |
| Python 3 | ≥ 3.8 | `python3 --version`（Windows：`python --version` 或 `py -3 --version`） | 实验后自动绘图 |
| matplotlib / numpy / pandas | — | `python3 -c "import matplotlib, numpy, pandas"` | 绘图库 |

```bash
# Python 绘图库缺失时安装（Windows 下用 pip，不带 3）
pip3 install matplotlib numpy pandas
```

```bash
git clone https://github.com/HuangLab-SYSU/agent-emulator.git
cd agent-emulator
go build ./...      # 首次编译，验证环境正常
```

不需要预先启动任何节点——集群由 agentemu 全自动拉起和回收。
绘图对字体无额外要求：图内文字使用 Times New Roman（macOS 与 Windows 系统自带；Linux 若无此字体自动回退到近似衬线字体）。

## 2. 五分钟上手

```bash
bash run_agentemu.sh
```

这一条命令会**依次自动完成**：

1. **编译**：`go build ./...`（先编译，编译失败不会清掉上一次的结果）
2. **清理旧结果**：删除 `./exp` 目录
3. **运行实验**：读取 `agentEmuConfig.yaml` → 编译 trace 为交易计划 → 自动启动集群（默认 4 分片 × 4 节点，万级交易约 1 分钟）→ 计划全部上链 → 集群自动停止
4. **自动绘图**：读取最新一轮的 Agent 账本 CSV，生成 23 张 PNG（详见第 7 节）到 `figs/figs_results/`
5. **生成 HTML 图册**并**自动在浏览器中打开**

用自定义配置运行：

```bash
bash run_agentemu.sh my-config.yaml
```

**Windows** 系统使用等价的批处理脚本，在 cmd 中运行（或在资源管理器中直接双击 `run_agentemu.bat`）：

```bat
run_agentemu.bat
rem 或指定配置：
run_agentemu.bat my-config.yaml
```

`run_agentemu.bat` 与 `.sh` 版行为完全一致：编译 → 清理旧结果 → 运行实验 → 自动绘图 → 在默认浏览器打开 HTML 图册。若 `python` 命令不可用，脚本会自动改用 `py -3`。

随时 `Ctrl-C` 可安全终止，集群子进程会被一并清理。

> **注意**：实验失败（非零退出）时不会执行绘图步骤；每次运行会先清空 `figs/figs_results/` 里的旧图，图册永远只反映最近一次成功的实验。

## 3. 编写 Trace 文件

JSONL 格式，一行一个动作，按 `ts` 排序（相同 `ts` 保持文件行序）。仓库自带三个示例：`traces/minimal.jsonl`（最小示例）、`traces/agent=100_txs=10000.jsonl`（默认，100 Agent 万笔交易）、`traces/pay_10k.jsonl`。

```json
{"agent_id":"agent-alice","action":"join","params_hash":"doc-alice-v1","ts":1}
{"agent_id":"agent-bob","action":"join","params_hash":"doc-bob-v1","ts":2}
{"agent_id":"agent-alice","action":"pay","target":"agent-bob","amount":12,"request_id":"payment-1","ts":3}
{"agent_id":"agent-bob","action":"leave","params_hash":"exit-bob","ts":5}
```

### 支持的行类型

| 类型 | 含义 | 编译成的交易 |
|---|---|---|
| `join` | Agent 上场，分配/恢复身份 | DID `register` 合约调用 |
| `pay` | 向另一个 Agent 转账 | **普通转账交易**（真实改变余额） |
| `leave` | Agent 离场 | DID `revoke` 合约调用 |
| 原始转账行 | 非 Agent 的裸转账 | 普通转账交易 |

原始转账行不含 `action` 字段，靠结构（出现 `sender` 键）自动识别，只有三个输入字段，无 `ts`（继承上一行的排序位置）：

```json
{"sender":"0xabc...","recipient":"0xdef...","value":"12345"}
```

### 必须遵守的规则

1. **`pay` 的双方（`agent_id` 和 `target`）当时必须处于 active 状态**（已 join 且未 leave），否则整场仿真报错终止——这是刻意的严格校验，防止无效交易污染区块。
2. `leave` 只能作用于 active 的 Agent；已离场的 Agent 再次 `join` 会重新注册。
3. `amount` 必须为正整数。链上新地址自动获得 10^36 初始余额，正常实验无需关心余额不足。
4. **trace 中永远不写 DID**——身份由 `seed + agent_id` 确定性派生，同 seed 同 ID 永远得到同一身份。
5. `request_id` 建议全局唯一——它是后续按"支付意图"检索数据的钥匙。

## 4. 配置说明

两层配置各管一边，互不干扰。

### agentEmuConfig.yaml（Agent 侧，当前默认值）

```yaml
base:
  blockemulator_config: ./config.yaml   # 集群模板：分片数/节点数/共识类型等
  result_dir: ./exp/agentemu-results    # 全部 agentemu 产物的根目录
  module_root: "."                      # 仓库根目录（构建集群二进制用）
experiment:
  seed: 20260903                        # DID 派生种子
  trace: ./traces/agent=100_txs=10000.jsonl   # trace 文件路径
chain:
  enabled: true                         # true：计划产出后自动启动集群上链
  run_timeout_seconds: 600              # 整场上链实验的硬性超时
  node_exit_grace_seconds: 15           # supervisor 退出后节点的优雅退出窗口
loop:
  max_rounds: 1                         # 轮次上限（多轮反馈为预留接口，默认单轮）
protocols:
  pay:
    plugin: direct-pay                  # 目前仅支持逐笔直付
  identity:
    plugin: did-simple
    contract_address: "0x0000000000000000000000000000000000000030"
```

### config.yaml（集群侧，BlockEmulator-X 原生配置）

agentemu 会以它为模板**派生每轮独立的集群配置**（自动重定向存储/日志路径、切换交易源为 `plan_source`、把 `tx_number` 钉在计划笔数）。常改的项：

- `system.shard_num` / `system.node_num`：集群规模——改成 `1`/`4` 即单分片轻量实验，ip_table 会自动生成匹配的端口表
- `consensus_node.block_interval`：出块间隔（ms）
- `system.log.log_level`：`info` / `warn` / `error`——**日志太吵时调成 warn**

## 5. 运行与观察

```bash
bash run_agentemu.sh            # 或 bash run_agentemu.sh <配置文件>
```

- 集群日志会**实时镜像到控制台**（每行带 `NodeInfo=S{分片}_N{节点}`，可分辨来源），完整日志同时保存在 `exp/agentemu-results/round_001/chain/logs/`
- 只想预览编译出的交易、不启动集群：把 `chain.enabled` 改为 `false`，几秒即出结果（此时无链上数据，也不会绘图）
- 结束时控制台打印各产物路径与图册路径（`figs/figs_results/index.html`）

## 6. 输出文件一览

```
exp/agentemu-results/
├── agent_registry.json            # agent_id ↔ DID 映射与 active 状态
├── rounds_summary.json            # 每轮记录数/交易数汇总
└── round_001/
    ├── agent_transactions.jsonl   # 交易计划（hash/双方/金额/nonce/data）
    ├── agent_action_txs.jsonl     # ★ 动作→交易映射（意图 ↔ 链上 hash）
    ├── Agent_Events.csv           # 逐动作事件流
    ├── agents/                    # ★ 每 Agent 账本（绘图的输入）
    │   ├── agent-001.csv
    │   └── ...                    #   每个 active Agent 一个文件
    └── chain/                     # 本轮集群（每轮隔离）
        ├── config.yaml / ip_table.json
        ├── logs/                  #   node_s0_n0.log ... supervisor.log
        └── results/
            ├── relay_stats_detail_tx_info.csv   # ★ 链上逐笔生命周期
            └── relay_stats_brief_info.csv       # 按 epoch 的 TPS/TCL 汇总
```

### Agent 账本 CSV（`agents/agent-XXX.csv`）格式

7 列，一行一笔该 Agent 相关的交易：

```
block_height, tx_hash, sender, recipient, value, balance, block_time_ms
```

- `balance` 为该交易执行后的余额，数值约 10^36、**超出 int64，需按字符串读入**（绘图脚本已处理）
- `block_time_ms` 为打包该交易的区块出块时间（上链时刻）毫秒
- 行序按区块出块时间排序，同毫秒内以分片/高度/块内序号决胜

**`agent_action_txs.jsonl`**（每个动作一行）：

```json
{"seq":3,"action":"pay","agent_id":"agent-alice","target":"agent-bob","amount":12,
 "ts":3,"request_id":"payment-1","params_hash":"","tx_hashes":["f58c994e..."]}
```

## 7. 自动绘图与 HTML 图册

### 7.1 实验结束后自动发生什么

`run_agentemu.sh`（Windows 为 `run_agentemu.bat`）在实验成功结束后自动执行：

1. 定位 `exp/agentemu-results/` 下**编号最大一轮**的 `agents/` 目录（当前单轮即 `round_001/agents`）
2. 清空 `figs/figs_results/` 里的旧图与旧 `index.html`（避免混入上一轮）
3. 运行 `figs/python_code/plot_agent_balance.py` 生成全部 PNG
4. 运行 `figs/python_code/build_fig_html.py` 生成图册页面
5. 调用系统 `open`（macOS）/ `xdg-open`（Linux）/ `start`（Windows）在默认浏览器打开 `figs/figs_results/index.html`

### 7.2 图的内容

图片内文字为英文、Times New Roman 字体。**页面上的图 1–4 编号按展示顺序排列**，与 PNG 文件名前缀的对应关系如下：

| 页面编号 | 内容 | PNG 文件 |
|---|---|---|
| 图 1 | 全部 Agent 总览：左"按 Agent ID"右"按 Δbalance 升序"两张期末余额柱状图（统一 y 轴） | `fig1_all_agents_overview.png` |
| 图 2 | 按全局交易顺序统计的全体 Agent 余额分布：tx_hash 去重后按确定性顺序回放 ±value，展示最小–最大包络、四分位距与均值线（封闭系统均值恒为 0） | `fig4_global_tx_order.png` |
| 图 3 | 按交易进度归一化对齐：各 Agent 自身交易序号拉伸到 0–1 后叠加，附终点均值标注 | `fig3_normalized_progress.png` |
| 图 4 | 分组余额轨迹：每 5 个 Agent 一张子图，含该 Agent 自己的区块分界虚线与星标 | `fig2_agents_001-005.png` … `fig2_agents_096-100.png` |

所有图绘制的是**相对初始余额的变化量 Δbalance = balance − 初始余额**（所有 Agent 初始余额相同且远大于波动幅度，画变化量才能看清细节）。

### 7.3 HTML 图册页面

- 顶部深色页眉：标题、生成时间、图表数量、数据来源目录与 Agent 数
- 图 1/2/3 整幅展示，图 4 为缩略图网格；**点击任意图片在新标签页打开原图**
- **中英文切换**：右上角按钮（当前中文时显示 "EN"，英文时显示"中文"），或按键盘 `L` 键；切换作用于页面标题、章节标题、元信息与页脚，语言偏好自动记忆，下次打开保持
- 图内文字由绘图脚本生成，不随页面语言切换变化

### 7.4 手动 / 独立运行绘图脚本

不重跑实验也可以随时重新画图。脚本路径按 `figs/python_code/` 相对定位，在仓库任意位置运行均可：

```bash
# 默认：自动选取最新一轮实验结果，输出到 figs/figs_results/
python3 figs/python_code/plot_agent_balance.py
python3 figs/python_code/build_fig_html.py

# 指定数据目录与输出目录（例如用历史快照重画）
python3 figs/python_code/plot_agent_balance.py \
    --data-dir exp/agentemu-results/round_001/agents \
    --fig-dir figs/figs_results
python3 figs/python_code/build_fig_html.py \
    --data-dir exp/agentemu-results/round_001/agents

# 只生成页面不重新画图
python3 figs/python_code/build_fig_html.py

# 打开图册
open figs/figs_results/index.html        # macOS
```

Windows 下等价命令（cmd，路径用反斜杠；`python` 不可用时改用 `py -3`）：

```bat
python figs\python_code\plot_agent_balance.py
python figs\python_code\build_fig_html.py

python figs\python_code\plot_agent_balance.py --data-dir exp\agentemu-results\round_001\agents --fig-dir figs\figs_results
python figs\python_code\build_fig_html.py --data-dir exp\agentemu-results\round_001\agents

rem 打开图册
start "" figs\figs_results\index.html
```

两个脚本的参数：

| 脚本 | 参数 | 默认值 | 说明 |
|---|---|---|---|
| `plot_agent_balance.py` | `--data-dir` | 自动选最新一轮 `agents/` | agent CSV 所在目录 |
| | `--fig-dir` | `figs/figs_results/` | PNG 输出目录 |
| `build_fig_html.py` | `--fig-dir` | `figs/figs_results/` | 扫描 PNG 并在此生成 `index.html` |
| | `--data-dir` | 无 | 仅用于页面显示数据来源与 Agent 数量 |

> `figs/figs_results/` 已加入 `.gitignore`，生成产物不会进入版本库；`figs/python_code/` 下的脚本是入库的。

## 8. 结果分析：按支付意图对账

三个文件联查即可回答"某笔支付是否确认、何时确认"：`agent_action_txs.jsonl`（request_id → tx hashes）→ `relay_stats_detail_tx_info.csv`（hash → 提交时间）。

```python
import json, csv

chain = {}
with open('exp/agentemu-results/round_001/chain/results/relay_stats_detail_tx_info.csv') as f:
    for row in csv.DictReader(f):
        chain[row['OriginalHash']] = row

total = confirmed = 0
for line in open('exp/agentemu-results/round_001/agent_action_txs.jsonl'):
    a = json.loads(line)
    if a['action'] != 'pay':
        continue
    total += 1
    if all(h in chain for h in a['tx_hashes']):
        confirmed += 1
        # 例：查某笔的确认时间
        # print(a['request_id'], chain[a['tx_hashes'][0]]['Tx finally commit time'])

print(f'支付意图 {confirmed}/{total} 全部确认')
```

延迟口径：`Tx finally commit time − Tx create time`；跨片交易在 CSV 中有 Relay1/Relay2 两段提议/提交时间可细分。

也可以直接用 pandas 分析 Agent 账本（注意 balance 按字符串读）：

```python
import pandas as pd
df = pd.read_csv('exp/agentemu-results/round_001/agents/agent-001.csv',
                 dtype={'balance': str, 'value': str})
```

## 9. 常见问题（FAQ）

**Q1：重跑报 `file already exists: .../block_record.csv`？**
上一次的产物没清理，而测量文件是独占创建的。`run_agentemu.sh` 已自动清理；手动运行 `go run cmd/agentemu/main.go` 前需自己 `rm -rf exp/agentemu-results`。

**Q2：为什么这次 join 没有生成注册交易？**
`agent_registry.json` 里该 Agent 已是 active。join 只在身份**首次出现或离场后重入**时生成注册交易。完整清理旧结果即可复现全量注册。

**Q3：转账是合约调用吗？**
不是。`pay` 编译为**普通转账**（直接改余额，不进 EVM）；`join`/`leave` 是合约调用形式。注意这些合约当前**未部署**，调用在 EVM 层是留痕（calldata 上链可回溯）而非真实合约状态——`pay` 的余额转移是真实生效的。

**Q4：控制台日志太多？**
`config.yaml` 里 `system.log.log_level: warn`，或运行时重定向 `bash run_agentemu.sh > run.log 2>&1`（文件里的集群日志始终完整）。

**Q5：想跑单分片小实验？**
`config.yaml` 的 `system.shard_num` 改为 `1`，其余不用动（ip_table 自动适配）。

**Q6：实验跑完但没有弹出图册页面？**
先看控制台末尾是否有 `figures & gallery: ./figs/figs_results/index.html`：
- 没有这行且出现 `warn: no agent CSVs ...`：本轮没有生成 Agent 账本（例如 `chain.enabled: false` 的预览运行），属正常
- 有这行但浏览器没弹：手动 `open figs/figs_results/index.html` 即可；无图形界面的服务器上请把目录拷回本地查看

**Q7：绘图脚本报 `ModuleNotFoundError: matplotlib`？**
`pip3 install matplotlib numpy pandas`，或换用已装好这些库的解释器（如 anaconda 的 python3）。

**Q8：想用历史一轮的数据重新画图？**
见 7.4 节，`--data-dir` 指向对应的 `round_XXX/agents/` 即可；注意 `run_agentemu.sh` 每次运行会清空 `exp/` 与 `figs/figs_results/`，历史数据需提前备份。

**Q9：图 4 的 20 张子图太多，能只看某几个 Agent 吗？**
当前按每 5 个 Agent 固定分组，分组逻辑在 `figs/python_code/plot_agent_balance.py` 的图 4 段（`range(0, len(dfs), 5)`），可自行修改分组大小后手动重跑（7.4 节）。

**Q10：结果可以复现吗？**
可以。同 seed + 同 trace + 同代码，计划文件、映射、事件 CSV 逐字节一致；Agent 账本按确定性全局顺序生成。链上侧的打包时序受运行时影响（与 BlockEmulator-X 本身一致），因此图 2 的中间包络形态每次可能略有不同，但期末值与守恒关系不变。

**Q11（Windows）：提示 "python 不是内部或外部命令"，或运行 python 却弹出了 Microsoft Store？**
说明 Python 未真正安装或未加入 PATH：从 [python.org](https://www.python.org/downloads/windows/) 安装时勾选 "Add python.exe to PATH"；或直接改用 Windows 自带的启动器 `py -3`（`run_agentemu.bat` 已自动回退到它）。安装后记得 `pip install matplotlib numpy pandas`。

## 10. 已知限制

- **合约为留痕模式**：DID 合约未部署，`join`/`leave` 调用是数据上链占位（合约部署在路线图上）；`pay` 的余额转移是真实生效的。
- **单轮运行**：多轮反馈循环（AgentAPI/EndCondition 接口）已预留但默认单轮，因此自动绘图固定取最新（即唯一）一轮。
- **错误即终止**：trace 中出现非法动作（如给未 join 的 Agent 转账）会终止整场仿真，错误信息带行号。
- **绘图依赖实验数据形状**：绘图脚本假设所有 Agent 初始余额相同（画 Δbalance 才有意义）；trace 中混入大量原始转账行时，图 2 的确定性回放顺序为示意（数据无区块内时间戳）。
