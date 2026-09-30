# AgentEmulator 优化方向

基于 agentsupervisor 包代码、README 路线图与 CI 工具链的现状调查，整理出以下优化方向。

## 一、实验指标能力

当前 Go 侧只产出计数类指标，延迟与吞吐数据散落在链侧 CSV，缺少聚合。对论文实验价值最大，建议优先。

- **端到端延迟与 TPS 汇总**：`rounds_summary.json` 只记录每轮 record/tx 数量，没有每轮耗时。所需数据均已存在——`agent_action_txs.jsonl` 提供 action 到 tx hash 的映射，链侧 `relay_stats_detail_tx_info.csv` 提供每笔交易 commit 时间戳，按 hash join 即可得到支付意图到上链确认的延迟分布与每轮吞吐。
- **计划-上链对账**：缺少计划交易数与实际提交交易数的核对，丢交易不可见。
- **raw_tx 事件补 request_id**：普通转账的 metric 事件不带 request_id，混合转账无法做支付意图关联。
- **补充 TPS 与延迟出图**：现有绘图只覆盖余额轨迹 fig1–fig4。

## 二、路线图功能扩展

- **DID 合约真实部署**：目前 join/leave 仅在 EVM 层记录，合约状态不更新，属 roadmap 近期项。
- **多轮反馈**：`AgentAPI` 仅有 `NoFeedback` 实现，`loop.max_rounds` 恒为 1，无法做跨轮实验。
- **交易调度策略**：v1.0 只有用户指定原始顺序一种策略，重排序、优先级与权重是预留给研究者的扩展点。

## 三、正确性与健壮性

- **CSV 转义缺陷**：`Agent_Events.csv` 用裸 `fmt.Fprintf` 写 request_id，含逗号、引号或换行会损坏文件，`LoadTrace` 也不校验该字段，应改用 `encoding/csv`；手写 JSONL 拼接同样脆弱。
- **输入校验缺口**：trace 单行超过 64KB 直接报错；pay 不拒绝自付与零金额；空 trace 把链侧 `tx_number` 钉在 0。
- **测试缺口**：`config.go` 无测试；chainrunner 的 `Run`、`Build` 与进程管理，registry 错误路径，`WriteResult` 错误分支均未覆盖。

## 四、性能与工程效率

- **每轮进程开销**：每轮启动 shard×node+1 个进程，固定约 8s 启动加最长 15s 收尾；实测有效共识约 22s，启停占轮时长近半。集群复用或压缩启停窗口对批量实验收益明显。
- **常数浪费**：每笔交易哈希两次；每轮重复解析 base YAML 三次；区块全量载入内存两份拷贝；shard 读取无并行。
- **工程链缺口**：无 Go benchmark；CI 不跑 `-race`，仅在 Go 文件变更的 PR 触发，Python 与 Docker 改动不测；Python 工具无依赖锁定；release 不构建 agentemu 驱动二进制；CLI 仅有 `-config`，换 seed、trace、轮数需改 YAML。

## 优先级建议

1. 指标聚合与对账：投入小，直接产出论文可用数据，同时修复 CSV 转义。
2. 多轮反馈与调度策略：研究性强，适合作为 forward 分支下一个主功能。
3. 运行效率优化：可后置。
