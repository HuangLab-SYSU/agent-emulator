# 计划：规范 exp 输出结构 + 新增 per-agent 交易 CSV

> 状态：已批准，实施于 2026-09-14。

## 1. 背景与目标

当前 `exp/` 输出存在两个问题：

1. **结构混乱**：`exp/agentemu-results/round_001/`（模拟侧产物）与
   `exp/agentemu-results/chain/round_001/`（链执行产物）两个同名目录，看目录
   名无法区分用途；编译出的二进制放在结果树的 `chain/bin/` 里，混淆了
   "结果"与"工具"。
2. **缺少 Agent 视角的链上数据**：现有 CSV（`Agent_Events.csv`、
   `relay_stats_*.csv`）都无法回答"某个 Agent 参与了哪些链上交易、每笔落在
   哪个区块、交易后余额多少"。

目标：

- 重构输出目录：二进制移到项目根目录，round 成为唯一顶层单元，链执行产物
  语义化地嵌套其中；
- 新增 per-agent CSV：每个 Agent 一个文件，记录与其相关的每笔已上链交易。

## 2. 目录重组

### 2.1 现状 → 新布局

```
现状                                      新布局
─────────────────────────                ─────────────────────────
exp/agentemu-results/                    <项目根>/consensusnode.exe      ← 二进制（gitignore）
  round_001/          模拟产物            <项目根>/supervisor.exe
  agent_registry.json                    exp/agentemu-results/
  rounds_summary.json                      agent_registry.json          （不变，跨轮共享）
  chain/                                   rounds_summary.json
    bin/*.exe          ← 移出              round_001/                    ★ 唯一 round 目录
    round_001/         ← 嵌套                agent_transactions.jsonl   交易计划
      config.yaml                           agent_action_txs.jsonl     意图→哈希映射
      ip_table.json                         Agent_Events.csv           事件
      node_s0_n0.log 等                     agents/agent-001.csv ...   ★ 新增 per-agent CSV
      boltdb/ trie_db/ block_record/        chain/                     本轮链执行
      results/                                config.yaml、ip_table.json
                                              logs/                    进程日志 + slog 日志
                                              data/                    boltdb/ trie_db/ block_record/
                                              results/                 relay_stats_*.csv 度量
```

### 2.2 代码改动（`agentemu/chainrunner.go` + `cmd/agentemu/main.go`）

| 项 | 现状 | 改为 |
|---|---|---|
| 二进制输出 | `WorkRoot/bin/` | `filepath.Join(c.ModuleRoot, binaryName(...))`，删除 `binDir()` |
| `WorkRoot` | `<result_dir>/chain`（main.go 拼接） | 直接传 `<result_dir>`（结果根） |
| 链执行目录 | `WorkRoot/round_%03d` | `WorkRoot/round_%03d/chain` |
| slog 日志目录 | roundDir 本身 | `chain/logs/` |
| 进程 .log | roundDir 本身 | `chain/logs/<name>.log` |
| boltdb / trie_db / block_record | roundDir 下平铺 | `chain/data/` 下 |
| supervisor 结果 | `roundDir/results` | `chain/results/` |
| `ChainOutcome` | `{RoundDir, ResultDir}` | 增加 `ChainDir`、`ShardNum`（供 CSV 收集器用） |

`.gitignore`：补 `/consensusnode`、`/supervisor`（`.exe` 已被现有 `**/*.exe`
覆盖）。

## 3. per-agent CSV（新文件 `agentemu/agentcsv.go`）

### 3.1 数据源与时机

集群自停后，从**已提交区块库**回读：每个分片打开 leader 的
`chain/data/boltdb/shard_S_node_0/block.db`（复用平台 `block.NewBoltStore` +
`GetNewestBlockHash` → `GetBlockByHash` → `block.DecodeBlock`，沿
`ParentBlockHash` 回溯到创世块，得到每分片升序区块序列）。

数据等价于"每个区块上链后记录一次"，但不改动平台内核代码（保持
AgentEmulator"零内核侵入"的设计原则）。在 `loop.go` 中 `ChainRunner.Run`
成功返回后调用，写入 `round_%03d/agents/`。

### 3.2 行规则

全局按 `(交易 CreateTime → 分片 → 高度 → 块内序)` 排序后单遍扫描，保证余额
推进确定性：

| 条件 | 动作 |
|---|---|
| sender 是 Agent 地址 且 `RelayStage != Relay2Tx` | 该 Agent 的 CSV 记一行（借记视角；register/revoke 合约交易金额 0 也计入） |
| recipient 是 Agent 地址 且（`RelayStage == Relay2Tx` 或 无 relay 标记的片内交易） | 记一行（贷记视角） |
| 片内交易双方都是 Agent | 两个 CSV 各一行（同区块同哈希） |

跨片 relay 的一笔逻辑转账：发送者 CSV 记 relay1 所在区块的行，接收者 CSV 记
relay2 所在区块的行——高度与时间各自真实，且不重复计数。

### 3.3 余额语义（复刻链上逻辑）

- 账户首次被触及时初始化为 `account.NormalInitBalanceStr`（10³⁹，与
  `pkg/chain/txexecute.go` 的懒初始化一致）；
- 借记 −value / 贷记 +value，行内 `balance` 为该笔交易**之后**的余额；
- 合约交易 value=0，余额不变。

### 3.4 文件与列

- 路径：`round_%03d/agents/<agent_id>.csv`（文件名做字符消毒，仅写过至少
  一行的 Agent；用 `encoding/csv` 写出）；
- 表头：`block_height,tx_hash,sender,recipient,value,balance,tx_time_ms`
  （时间戳为链上 CreateTime 毫秒——plansource 注入时重打，即真实上链时刻，
  非 trace 的 ts）；
- 地址→Agent 映射来自 Host 的 registry（`agent_registry.json`）。

## 4. 配套更新

- `agentemu/chainrunner_test.go`：路径断言改为新布局；
- 新增 `agentemu/agentcsv_test.go`：合成区块验证——片内双向行、relay1/relay2
  分行且 relay2 不重复记发送方、合约交易余额不变、懒初始化、排序确定性；
- `run_agentemu.sh` / `run_agentemu.bat`：结尾路径提示同步；
- `README.md`：AgentEmulator 章节的输出表更新。

## 5. 验证

1. `go build ./...`、`go test -gcflags=all='-N -l' ./...`、golangci-lint、
   `go mod tidy`；
2. 端到端 `run_agentemu.bat`（10k trace）：确认新目录树；抽查
   `agent-001.csv`——register 行余额 = 10³⁹、转账行余额逐笔递变且金额与
   `agent_action_txs.jsonl` 对账、哈希可在 `relay_stats_detail_tx_info.csv`
   回查；核对某 Agent 期末余额 = 初始 + Σ收 − Σ支。

## 6. 对用户可见的行为变化

- 二进制出现在项目根目录（重复运行直接覆盖，不再随 `rm -rf exp` 删除）；
- 旧布局的 `exp/agentemu-results/chain/` 不再产生；首轮新运行会重建整个
  `exp/`；
- 每个 Agent 多一个专属 CSV，含区块高度与逐笔余额。
