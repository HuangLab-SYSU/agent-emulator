# 指标聚合 + CSV 转义修复 实施计划

基于 forward 分支，分三个独立提交实施。

## 提交 1：`fix: escape Agent_Events.csv fields via encoding/csv`

问题：`agentsupervisor/agentsupervisor.go` 的 `writeResult` 用裸
`fmt.Fprintf("%s,%d,%s,%d")` 写 `Agent_Events.csv`，request_id 含逗号、引号或换行会损坏文件。

改动：

- `writeResult` 中 Agent_Events.csv 段改为 `encoding/csv`，仿照同包
  `agentcsv.go` 的 `writeAgentCSV` 模式：包级 header 变量、`csv.NewWriter`、
  逐行 `w.Write`、`w.Flush()` + `return w.Error()`。
- 顺带把同函数里手写的 plan JSONL（`fmt.Fprintf` 拼 JSON）改为 `json.Marshal`
  一个字段序一致的 `planLine` 结构体，输出字节格式不变，链侧 jsonsource
  回放不受影响。
- 新增测试：构造含逗号、引号、换行的 request_id 走完 `Process` +
  `WriteResult`，用 `csv.NewReader` 读回断言字段原样往返。

## 提交 2：`feat: attribute plain transfers with request_id`

问题：普通转账行的 request_id 在 LoadTrace 合成 Record 时被丢弃，
`raw_tx` metric 事件永远没有 request_id，混合转账无法做支付意图关联。

改动：

- `trace.go` 转账分支：把 `probe.Record.RequestID` 复制进合成的 Record。
- `processRawTx` 中 `raw_tx` 的 MetricEvent 补上 `RequestID`；不能直接用
  `metric()` 助手，因其 Value 取 `record.Amount` 恒为 0。
- `scripts/gen_pay_trace.py`：混合转账行 emit 补
  `"request_id": f"raw-{n:05d}"`。
- 用相同参数重新生成 `traces/random_mix_traces.jsonl`：确定性输出，
  request_id 不参与交易编码，交易哈希不变。
- README.md 普通转账段落补一句：行可携带可选 `request_id`。
- 测试：含 request_id 的转账行，Record、ActionTxLink、MetricEvent 三处
  均携带。

## 提交 3：`feat: aggregate per-round chain metrics into rounds_summary`

核心：新文件 `agentsupervisor/roundmetrics.go`，在 `loop.go` 链运行成功后
调用，仅 `Chain != nil` 时执行。

数据流：

- 输入：`sup.links`（内存中的 ActionTxLink 切片，无需重读 JSONL）+
  `outcome.ResultDir` 下的 `relay_stats_detail_tx_info.csv` 与
  `relay_stats_brief_info.csv`。
- 读 detail CSV：取 `OriginalHash`、`Tx create time`、
  `Tx finally commit time`、`Is cross-shard tx or not`；时间按 RFC3339 解析。
- 与 links 的 tx_hashes 做 join：每笔已提交交易反向归属到 action 类型。

新结构 `RoundMetrics`，挂到 `RoundResult.Metrics`，自动进入
rounds_summary.json：

- 对账：`tx_planned`、`tx_committed`、`tx_missing`、`inner_shard_tx`、
  `cross_shard_tx`。
- 延迟：commit 减 create 的 avg、p50、p95、max，整体加按 action 类型分组；
  detail CSV 为秒级精度，文档注明局限。
- 吞吐：`throughput_tps` = committed 除以 min create 到 max commit 时间跨度。
- 链侧纪元透传：`chain_epochs` 原样带过 brief CSV 的每纪元 Avg TPS、
  CTX ratio、纳秒级 TCL。
- 计时：轮次开头 `time.Now()` 记 `round_wall_seconds`，链运行前后记
  `chain_wall_seconds`。

测试：构造 fixture detail/brief CSV 加 links，断言对账计数、延迟统计、
missing 检出、tps、纪元透传、时间解析错误路径；Chain 为 nil 时 Metrics
为空。

## 验证与收尾

1. `go build ./...`、`go test ./...`、`golangci-lint run ./...` 全绿。
2. 端到端验证：`go run ./cmd/agentemu -config agentEmuConfig.yaml` 跑一轮
   真实实验，确认 `rounds_summary.json` 出现 metrics 字段、`tx_missing=0`、
   Agent_Events.csv 正常。
3. 三个提交依次落到 forward 分支并推送。
