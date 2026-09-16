# AgentEmulator 用户使用指南

AgentEmulator 是构建在 BlockEmulator-X 之上的 **Trace 驱动的 Agent 行为仿真器**：你用 JSONL 文件描述一段 Agent 剧情（加入、转账、记日志、退出），它把剧情编译成真正的区块链交易，然后**自动启动一个私有的 BlockEmulator-X 集群**把交易全部上链，最后留下完整的计划和测量数据。

```
trace 文件（join / pay / append_log / leave）
        │  agentemu 编译（确定性 DID、审计锚定）
        ▼
交易计划 agent_transactions.jsonl
        │  ChainRunner 自动构建二进制、派生配置、拉起集群
        ▼
BlockEmulator-X 集群（plan_source 注入 → 共识 → 上链 → 自动收尾）
        ▼
计划 / 意图映射 / 注册表 / 链上测量 / 轮次汇总
```

---

## 1. 环境准备

- **Go ≥ 1.25**（`go version` 确认）
- macOS / Linux 均可（本机回环网络）

```bash
git clone https://github.com/HuangLab-SYSU/agent-emulator.git
cd agent-emulator
go build ./...      # 首次编译，验证环境正常
```

不需要预先启动任何节点——集群由 agentemu 全自动拉起和回收。

## 2. 快速开始

```bash
bash run_agentemu.sh
```

这一条命令会依次完成：编译 → 清理旧结果 → 读取 `agentEmuConfig.yaml` → 编译 trace 为交易计划 → 自动启动集群（默认 4 分片 × 4 节点，跟随 `config.yaml`）→ 计划全部上链 → 集群自动停止 → 打印结果位置。整个过程约 1 分钟（万级交易规模）。

用自定义配置运行：

```bash
bash run_agentemu.sh my-config.yaml
```

随时 `Ctrl-C` 可安全终止，集群子进程会被一并清理。

## 3. 编写 Trace 文件

JSONL 格式，一行一个动作，按 `ts` 排序（相同 `ts` 保持文件行序）：

```json
{"agent_id":"agent-alice","action":"join","params_hash":"doc-alice-v1","ts":1}
{"agent_id":"agent-bob","action":"join","params_hash":"doc-bob-v1","ts":2}
{"agent_id":"agent-alice","action":"pay","target":"agent-bob","amount":12,"request_id":"payment-1","ts":3}
{"agent_id":"agent-alice","action":"append_log","params_hash":"request-1","request_id":"payment-1","ts":4}
{"agent_id":"agent-bob","action":"leave","params_hash":"exit-bob","ts":5}
```

### 四种动作

| action | 含义 | 编译成的交易 |
|---|---|---|
| `join` | Agent 上场，分配/恢复身份 | DID `register` 合约调用（+ 审计条目） |
| `pay` | 向另一个 Agent 转账 | **普通转账交易**（+ 审计条目） |
| `append_log` | 记一条行为日志 | `merkle-audit`：攒批后 `anchor` 锚定；`onchain-audit`：逐条 `append` |
| `leave` | Agent 离场 | DID `revoke` 合约调用（+ 审计条目） |

### 必须遵守的规则

1. **`pay` 的双方（`agent_id` 和 `target`）当时必须处于 active 状态**（已 join 且未 leave），否则整场仿真报错终止——这是刻意的严格校验，防止生成无效交易污染区块（块内一笔失败会导致整块失败）。
2. `leave` 只能作用于 active 的 Agent；已离场的 Agent 再次 `join` 会重新注册（生成新的 `register` 交易）。
3. `amount` 必须为正整数。链上新地址会自动获得 10^36 初始余额，正常实验无需关心余额不足。
4. **trace 中永远不写 DID**——身份由 `seed + agent_id` 确定性派生（`did:broker:0x` + sha256 后 20 字节），同 seed 同 ID 永远得到同一身份，且跨轮次/跨运行稳定。
5. `request_id` 建议全局唯一——它是后续按"支付意图"检索数据的钥匙。

### 批量生成：trace 生成器

`scripts/gen_pay_trace.py` 可生成大规模合法 trace（自动维护活跃集，保证所有转账合法）：

```bash
python3 scripts/gen_pay_trace.py --agents 100 --pays 10000 \
    --seed 20260912 --out traces/pay_10k.jsonl
```

参数：`--agents`（Agent 数）、`--pays`（转账笔数）、`--seed`（随机种子，决定内容）、`--out`（输出文件）、`--lifecycle-every`（每 N 笔转账穿插一次重入+离场，默认 250，保证活跃集不枯竭）。同参数输出逐字节一致。

## 4. 配置说明

两层配置各管一边，互不干扰：

### agentEmuConfig.yaml（Agent 侧）

```yaml
base:
  blockemulator_config: ./config.yaml   # 集群模板：分片数/节点数/共识类型等
  result_dir: ./exp/agentemu-results    # 全部 agentemu 产物的根目录
  module_root: "."                      # 仓库根目录（构建集群二进制用）
experiment:
  seed: 20260903                        # DID 派生种子
  trace: ./traces/minimal.jsonl         # trace 文件路径
chain:
  enabled: true                         # true：计划产出后自动启动集群上链
  run_timeout_seconds: 600              # 整场上链实验的硬性超时
  node_exit_grace_seconds: 15           # supervisor 退出后节点的优雅退出窗口
loop:
  max_rounds: 1                         # 轮次上限（多轮反馈为预留接口，默认单轮）
protocols:
  pay:
    plugin: direct-pay                  # 目前仅支持逐笔直付
  audit:
    plugin: merkle-audit                # merkle-audit（攒批锚定）| onchain-audit（逐条）
    contract_address: "0x0000000000000000000000000000000000000020"
    batch_size: 2                       # merkle 模式的攒批大小
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

- 集群日志会**实时镜像到控制台**（每行带 `NodeInfo=S{分片}_N{节点}`，可分辨来源），完整日志同时在 `exp/agentemu-results/chain/round_001/*.log`
- 只想预览编译出的交易、不启动集群：把 `chain.enabled` 改为 `false`，几秒即出结果
- 结束时控制台打印各产物路径

## 6. 输出文件一览

```
exp/agentemu-results/
├── agent_registry.json            # agent_id ↔ DID 映射与 active 状态
├── rounds_summary.json            # 每轮记录数/交易数汇总
├── round_001/
│   ├── agent_transactions.jsonl   # 交易计划（hash/双方/金额/nonce/data）
│   ├── agent_action_txs.jsonl     # ★ 动作→交易映射（意图 ↔ 链上 hash）
│   └── Agent_Events.csv           # 逐动作事件流
└── chain/round_001/
    ├── config.yaml / ip_table.json  # 本轮派生的集群配置（每轮隔离）
    ├── node_s0_n0.log ... supervisor.log
    └── results/
        ├── relay_stats_detail_tx_info.csv   # ★ 链上逐笔生命周期
        └── relay_stats_brief_info.csv       # 按 epoch 的 TPS/TCL 汇总
```

**`agent_action_txs.jsonl`**（每个动作一行）：

```json
{"seq":3,"action":"pay","agent_id":"agent-alice","target":"agent-bob","amount":12,
 "ts":3,"request_id":"payment-1","params_hash":"",
 "tx_hashes":["f58c994e...","7b44dfb0..."]}
```

一笔 `pay` 对应转账交易 + 它所在批次的审计锚定交易；merkle 锚定覆盖一批动作的日志，因此**同一个锚定 hash 会出现在所有被覆盖动作的行里**（多对多归因）。

## 7. 结果分析：按支付意图对账

三个文件联查即可回答"某笔支付是否确认、何时确认"：`agent_action_txs.jsonl`（request_id → tx hashes）→ `relay_stats_detail_tx_info.csv`（hash → 提交时间）。

```python
import json, csv

chain = {}
with open('exp/agentemu-results/chain/round_001/results/relay_stats_detail_tx_info.csv') as f:
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

延迟口径：`Tx finally commit time - Tx create time`；跨片交易在 CSV 中有 Relay1/Relay2 两段提议/提交时间可细分。

## 8. 常见问题（FAQ）

**Q1：重跑报 `file already exists: .../block_record.csv`？**
上一次的产物没清理，而测量文件是独占创建的。`run_agentemu.sh` 已自动清理；手动运行 `go run cmd/agentemu/main.go` 前需自己 `rm -rf exp/agentemu-results`。

**Q2：为什么这次 join 没有生成注册交易？**
`agent_registry.json` 里该 Agent 已是 active。join 只在身份**首次出现或离场后重入**时生成注册交易。完整清理旧结果即可复现全量注册。

**Q3：转账是合约调用吗？**
不是。`pay` 编译为**普通转账**（data 为空，直接改余额，不进 EVM）；`join`/`leave`/审计才是合约调用形式。注意这些合约当前**未部署**，调用在 EVM 层是留痕（calldata 上链可回溯）而非真实合约状态——`pay` 的余额转移是真实生效的。

**Q4：控制台日志太多？**
`config.yaml` 里 `system.log.log_level: warn`，或运行时重定向 `bash run_agentemu.sh > run.log 2>&1`（文件里的集群日志始终完整）。

**Q5：想跑单分片小实验？**
`config.yaml` 的 `system.shard_num` 改为 `1`，其余不用动（ip_table 自动适配）。

**Q6：结果可以复现吗？**
可以。同 seed + 同 trace + 同代码，计划文件、映射、事件 CSV 逐字节一致。链上侧的打包时序受运行时影响（与 BlockEmulator-X 本身一致）。

## 9. 已知限制

- **合约为留痕模式**：DID/审计合约未部署，相关调用是数据上链占位（合约部署在路线图上）；因此涉及"合约执行开销"的测量目前不含真实 EVM 成本。
- **单轮运行**：多轮反馈循环（AgentAPI/EndCondition 接口）已预留但默认单轮。
- **错误即终止**：trace 中出现非法动作（如给未 join 的 Agent 转账）会终止整场仿真，错误信息带行号。
