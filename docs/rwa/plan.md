# AgentEmulator RWA 买卖交易开发方案

## 1. 背景与目标

当前 AgentEmulator 支持 `join`、`leave`、`pay` 和普通转账 trace，`agentSupervisor` 将 trace 编译为 `agent_transactions.jsonl`，再由 `../../cmd/supervisor` 读取并发送到各分片共识节点执行。实验结束后，系统从各分片区块存储读取已提交交易，生成 Agent CSV 和可视化图表。

本期新增 RWA 算力买卖演示能力，模拟“AI 智能体以港元稳定币微支付购买 RWA 数据/算力”的场景。链上只负责记录买卖行为和支付交易，实验结束后汇总所有分片区块中的 RWA 买卖交易，生成分析数据和图表，提供给监管审计部门查看。系统本身不执行审计动作。

本期范围：

- 新增 `buy` 和 `sell` 两种 agent trace action。
- 不实现订单簿，不做撮合，不做买卖挂单匹配。
- `sell` 表示卖方声明可出售的算力资源，交易 `to` 为空，`value` 为空或链内归一化为 `0`。
- `buy` 表示买方向某个卖方 agent 购买算力，`to` 为卖方 agent，`value = 单份算力价格 * 算力份数`。
- `sell` 额外携带 `compute_id`、`unit_price`、`quantity`。
- `buy` 额外携带 `compute_id`、`quantity`，并从对应卖方报价计算 `value`。
- `buy` 和 `sell` 都作为普通交易上链，不涉及智能合约部署和调用。
- 即使买方账户余额不足，也不影响交易进入区块。
- `buy` 和 `sell` 交易只发送到源 agent 所在分片，不发送到目标 agent 所在分片。
- 实验结束后汇总所有分片的区块，解析 RWA 买卖交易，输出 CSV，并生成箱线图等图表。

本期不包含：

- 订单簿、撮合引擎、报价优先级、成交规则。
- 智能合约部署、智能合约调用、链上撮合。
- 真实审计流程。
- 对 RWA 所有权、库存、余额约束的强校验。
- 生产级监管报送接口。

## 2. 现状确认

当前 trace 解析入口在 `../../agentsupervisor/trace.go`。现有 `Action` 只有 `pay`、`join`、`leave` 和内部合成的 `raw_tx`：

```go
const (
	ActionPay   Action = "pay"
	ActionJoin  Action = "join"
	ActionLeave Action = "leave"
	ActionRawTx Action = "raw_tx"
)
```

当前 agent trace 的核心结构为：

```go
type Record struct {
	AgentID    string     `json:"agent_id"`
	Action     Action     `json:"action"`
	Target     string     `json:"target"`
	Amount     uint64     `json:"amount"`
	TS         int64      `json:"ts"`
	RequestID  string     `json:"request_id"`
	ParamsHash string     `json:"params_hash"`
	RawTx      *RawTxSpec `json:"-"`
	Seq        int        `json:"-"`
}
```

当前 trace 编译为交易计划的主要逻辑在 `../../agentsupervisor/agentsupervisor.go`。`pay` 会校验付款方和收款方都是 active agent，然后生成一笔普通转账：

```go
s.appendTx(from, to, record.Amount, nil, record.TS)
```

当前 `agent_transactions.jsonl` 的读取逻辑在 `../../supervisor/txsource/tracesource/tracesource.go`，只读取基础交易字段：

```go
type traceLine struct {
	Hash      string `json:"hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Value     string `json:"value"`
	Nonce     uint64 `json:"nonce"`
	Data      string `json:"data"`
}
```

当前交易结构在 `../../pkg/core/transaction/transaction.go`，核心字段是：

```go
type Transaction struct {
	Sender      account.Address
	Recipient   account.Address
	Value       *big.Int
	PriorityFee *big.Int
	Nonce       uint64
	Signature   Signature
	CreateTime  time.Time
	Data        []byte
	GasLimit    uint64
	RelayTxOpt
	BrokerTxOpt
}
```

需要特别注意：当前 `TxType()` 通过 `Data` 判断交易类型。如果 `Data` 非空且 `Recipient` 非空，会被识别为 `CallContractTxType`；如果 `Data` 非空且 `Recipient` 为空，会被识别为 `CreateContractTxType`。因此本期不能简单把 RWA 额外字段塞进 `Data`，否则会被误判为合约交易，违背“不涉及智能合约部署和调用”。

当前分片发送逻辑主要在 `../../supervisor/committee` 下。`static_relay` 模式下使用发送方地址定位分片，基本符合“只发送到源 agent 所在分片”的要求；但 broker 模式和 CLPA 相关模式中可能会依据收发双方关系生成跨分片 relay/broker 交易，因此需要对 RWA 买卖交易做明确绕过或限制。

当前区块结果读取和 agent CSV 输出在 `../../agentsupervisor/agentcsv.go`。它已经具备遍历所有分片 leader 区块存储的能力，可以复用为 RWA 汇总分析的数据入口。

当前图表入口在 `../../figs/python_code/plot_agent_balance.py` 和 `../../figs/python_code/build_fig_html.py`，运行脚本在 `../../run_agentemu.sh`。

## 3. 总体设计

本期采用“trace 扩展 + 普通交易扩展字段 + 实验后离线汇总”的设计，不引入订单簿，也不引入智能合约。

总体链路如下：

```text
trace.jsonl
  -> agentsupervisor 解析 buy / sell
  -> 编译为普通 Transaction
  -> agent_transactions.jsonl 保留 RWA 扩展字段
  -> cmd/supervisor 读取交易
  -> 按 sender 所在分片发送给共识节点
  -> 共识节点打包普通交易
  -> 实验结束后读取所有分片区块
  -> 汇总 RWA buy / sell 交易
  -> 输出 rwa_orders.csv / rwa_trades.csv / rwa_summary.csv
  -> 绘制箱线图、成交量图、Agent 买卖统计图
```

核心设计原则：

- `buy` 和 `sell` 都是普通交易，不进入合约执行路径。
- RWA 元数据不放入 `tx.Data`，避免触发合约交易类型判断。
- RWA 元数据作为 `Transaction` 的可选扩展字段随交易 RLP 编码进入区块。
- `sell` 的链内 `Recipient` 使用空地址，`Value` 使用 `0`，但 JSONL 可以保留空 `recipient` 和空 `value` 的语义。
- `buy` 的 `Recipient` 使用卖方 agent DID 地址，`Value` 使用 `unit_price * quantity`。
- 余额不足不影响上链，执行层对 RWA `buy` 交易不因余额不足返回错误。
- 分片路由只看 `Sender`，不因 `Recipient` 跨分片生成 relay/broker 交易。
- 实验分析以区块存储为准，而不是以 trace 或交易计划文件为准。

## 4. 数据模型

### 4.1 Trace 输入格式

新增 `sell` trace 示例：

```json
{"agent_id":"provider-001","action":"sell","compute_id":"gpu-a100-hour","unit_price":"500000","quantity":100,"request_id":"sell-001","ts":1000}
```

新增 `buy` trace 示例：

```json
{"agent_id":"buyer-001","action":"buy","target":"provider-001","compute_id":"gpu-a100-hour","quantity":10,"request_id":"buy-001","ts":2000}
```

字段含义：

| 字段 | 适用动作 | 含义 |
| --- | --- | --- |
| `agent_id` | `buy` / `sell` | 源 agent ID，即交易 sender |
| `action` | `buy` / `sell` | 行为类型 |
| `target` | `buy` | 卖方 agent ID |
| `compute_id` | `buy` / `sell` | 算力或 RWA 资源 ID |
| `unit_price` | `sell` | 单份算力价格，使用字符串整数 |
| `quantity` | `buy` / `sell` | 算力份数，正整数 |
| `request_id` | `buy` / `sell` | 行为请求 ID，用于 trace、交易和图表关联 |
| `ts` | `buy` / `sell` | 逻辑时间 |
| `params_hash` | 可选 | 参数版本标识，保留现有语义 |

价格建议使用港元稳定币最小单位，避免浮点误差。例如：

```text
1 HKD = 1_000_000 microHKD
unit_price = 每份算力的 microHKD 价格
value = unit_price * quantity
```

### 4.2 Transaction 扩展字段

建议在 `../../pkg/core/transaction/transaction.go` 中新增 RWA 可选扩展字段，例如：

```go
type RWATxOpt struct {
	Action    string
	AgentID   string
	TargetID  string
	RequestID string
	ComputeID string
	UnitPrice *big.Int
	Quantity  uint64
}
```

并在 `Transaction` 中增加：

```go
RWATxOpt
```

设计要求：

- `Action` 只允许为空、`buy`、`sell`。
- 普通交易 `Action` 为空。
- RWA 交易 `Data` 必须为空。
- `TxType()` 不应因为 RWA 字段非空而返回合约类型。
- RWA 字段应参与 RLP 编码，从而随交易进入区块。
- 如果兼容性需要更强，可将 RWA 字段设计为单个 `Extra` 结构或 `TxMeta` 结构，后续可扩展其他演示元数据。

### 4.3 交易语义

`sell` 交易语义：

| 字段 | 值 |
| --- | --- |
| `Sender` | 卖方 agent DID 地址 |
| `Recipient` | `account.EmptyAccountAddr` |
| `Value` | `0` |
| `Data` | 空 |
| `RWATxOpt.Action` | `sell` |
| `RWATxOpt.ComputeID` | 算力 ID |
| `RWATxOpt.UnitPrice` | 单份算力价格 |
| `RWATxOpt.Quantity` | 出售算力份数 |

`buy` 交易语义：

| 字段 | 值 |
| --- | --- |
| `Sender` | 买方 agent DID 地址 |
| `Recipient` | 卖方 agent DID 地址 |
| `Value` | `sell.unit_price * buy.quantity` |
| `Data` | 空 |
| `RWATxOpt.Action` | `buy` |
| `RWATxOpt.ComputeID` | 算力 ID |
| `RWATxOpt.Quantity` | 购买算力份数 |
| `RWATxOpt.TargetID` | 卖方 agent ID |

## 5. 卖价引用规则

由于取消订单簿，`buy` 不指定出价，金额由对应卖方 `sell` 设置的单份算力价格计算。因此 `agentSupervisor` 需要维护一个轻量的卖方报价索引，不做撮合，只做价格查询。

索引 key 建议为：

```text
seller_agent_id + compute_id
```

索引 value 为：

```text
unit_price
latest_sell_request_id
latest_sell_ts
available_quantity_for_display
```

处理规则：

- `sell` 行出现时，更新该卖方该算力 ID 的最新报价。
- `buy` 行出现时，按 `target + compute_id` 查找最新卖价。
- 找不到对应卖价时，trace 编译失败，提示缺少卖方报价。
- `buy.quantity` 必须为正整数。
- `value = unit_price * buy.quantity`。
- 本期不扣减库存，不校验卖方剩余份数。
- 本期不校验买方余额，不因余额不足阻止交易计划生成。
- 如果同一卖方同一 `compute_id` 多次 `sell`，后续 `buy` 使用逻辑时间排序后的最新报价。

这不是订单簿，因为它不维护买盘、不排序、不撮合、不选择最优卖家，只是按用户明确指定的卖方和资源 ID 查询价格。

## 6. 功能开发步骤

### 6.1 Trace 解析扩展

目标：支持 `buy` 和 `sell` 两种 action，并完成基础字段校验。

改造 `../../agentsupervisor/trace.go`。

新增 action：

```go
const (
	ActionBuy  Action = "buy"
	ActionSell Action = "sell"
)
```

扩展 `Record`：

```go
ComputeID string `json:"compute_id"`
UnitPrice string `json:"unit_price"`
Quantity  uint64 `json:"quantity"`
```

校验规则：

- `buy` 必须包含 `agent_id`、`target`、`compute_id`、`quantity`、`ts`。
- `buy` 不需要 `amount`，也不需要 `unit_price`。
- `sell` 必须包含 `agent_id`、`compute_id`、`unit_price`、`quantity`、`ts`。
- `sell` 不需要 `target`，不需要 `amount`。
- `unit_price` 使用十进制字符串解析为非负大整数。
- `quantity` 必须大于 `0`。
- `buy` 和 `sell` 的 `agent_id` 必须是 active agent。
- `buy.target` 必须是 active agent。
- 原有 `pay`、`join`、`leave`、plain transfer 行为保持兼容。

需要补充 `../../agentsupervisor/trace_test.go`：

- 正常解析 `sell`。
- 正常解析 `buy`。
- `sell` 缺少 `compute_id` 报错。
- `sell` 缺少 `unit_price` 报错。
- `buy` 缺少 `target` 报错。
- `buy` 缺少报价时在编译阶段报错。
- 相同 `ts` 下仍按文件顺序稳定排序。

### 6.2 AgentSupervisor 编译扩展

目标：将 `buy` / `sell` 编译为普通交易，并保留 RWA 元数据。

改造 `../../agentsupervisor/agentsupervisor.go`。

新增内部报价缓存：

```go
type sellQuote struct {
	SellerAgentID string
	SellerAddr    account.Address
	ComputeID     string
	UnitPrice     *big.Int
	Quantity      uint64
	RequestID     string
	TS            int64
}

quotes map[string]sellQuote
```

新增处理分支：

```go
case ActionSell:
	return s.processSell(record)
case ActionBuy:
	return s.processBuy(record)
```

`sell` 编译逻辑：

```text
1. 校验 seller active。
2. 解析 seller DID address。
3. 解析 unit_price。
4. 更新 quotes[seller_agent_id + compute_id]。
5. 构造普通交易：
   from = seller address
   to = empty address
   value = 0
   data = nil
   rwa action = sell
   compute_id = record.compute_id
   unit_price = record.unit_price
   quantity = record.quantity
6. 写入 action -> tx hash 映射。
7. 记录 metric kind = rwa_sell_onchain。
```

`buy` 编译逻辑：

```text
1. 校验 buyer active。
2. 校验 seller target active。
3. 按 seller_agent_id + compute_id 查找最新 sell 报价。
4. value = quote.unit_price * record.quantity。
5. 构造普通交易：
   from = buyer address
   to = seller address
   value = computed value
   data = nil
   rwa action = buy
   compute_id = record.compute_id
   quantity = record.quantity
   target_id = seller agent id
6. 不检查 buyer 余额。
7. 不检查 seller 库存。
8. 写入 action -> tx hash 映射。
9. 记录 metric kind = rwa_buy_onchain。
```

需要扩展 `ActionTxLink`，保留 RWA 字段：

```go
ComputeID string `json:"compute_id,omitempty"`
UnitPrice string `json:"unit_price,omitempty"`
Quantity  uint64 `json:"quantity,omitempty"`
```

### 6.3 交易结构扩展

目标：让 RWA 元数据随普通交易进入区块，但不触发合约执行。

改造 `../../pkg/core/transaction/transaction.go`。

推荐新增：

```go
type RWATxOpt struct {
	RWAAction string
	AgentID   string
	TargetID  string
	RequestID string
	ComputeID string
	UnitPrice *big.Int
	Quantity  uint64
}
```

并在 `Transaction` 结构中加入：

```go
RWATxOpt
```

`TxType()` 保持以 `Data` 和 relay/broker 字段判断，不因为 `RWATxOpt` 非空改变类型。对于 RWA 交易，只要 `Data` 为空，就返回 `NormalTxType`。

需要新增辅助方法，减少字符串散落：

```go
func (tx *Transaction) IsRWATx() bool
func (tx *Transaction) IsRWABuy() bool
func (tx *Transaction) IsRWASell() bool
```

约束：

- `sell` 虽然 `Recipient` 是空地址，但 `Data` 为空，所以必须仍然是普通交易。
- `buy` 有 `Recipient` 和 `Value`，`Data` 为空，也必须仍然是普通交易。
- RWA 字段应参与 `Encode()` 和 `Hash()`，确保链上交易哈希覆盖 RWA 元数据。

### 6.4 交易计划 JSONL 扩展

目标：`agent_transactions.jsonl` 能完整保存和回放 RWA 交易。

改造 `../../agentsupervisor/agentsupervisor.go` 中写出交易计划的逻辑，以及 `../../supervisor/txsource/tracesource/tracesource.go` 中读取交易计划的逻辑。

JSONL 建议格式：

`sell` 计划行：

```json
{
  "hash": "0x...",
  "sender": "0x...",
  "recipient": "",
  "value": "",
  "nonce": 1,
  "data": "0x",
  "rwa_action": "sell",
  "agent_id": "provider-001",
  "request_id": "sell-001",
  "compute_id": "gpu-a100-hour",
  "unit_price": "500000",
  "quantity": 100
}
```

`buy` 计划行：

```json
{
  "hash": "0x...",
  "sender": "0x...",
  "recipient": "0x...",
  "value": "5000000",
  "nonce": 2,
  "data": "0x",
  "rwa_action": "buy",
  "agent_id": "buyer-001",
  "target_id": "provider-001",
  "request_id": "buy-001",
  "compute_id": "gpu-a100-hour",
  "quantity": 10
}
```

读取规则：

- 普通交易仍按原字段读取。
- `rwa_action == "sell"` 时，允许 `recipient` 为空，链内设置为 `account.EmptyAccountAddr`。
- `rwa_action == "sell"` 时，允许 `value` 为空，链内设置为 `0`。
- `rwa_action == "buy"` 时，`recipient` 和 `value` 必须存在。
- `unit_price` 为空时，普通交易不受影响。
- `data` 必须为空或 `0x`，否则 RWA 交易拒绝读取，避免误入合约路径。

需要补充 `../../supervisor/txsource/tracesource/tracesource_test.go`：

- 能读取 `sell` 空 recipient / 空 value。
- 能读取 `buy` RWA 元数据。
- RWA 交易 `TxType()` 是 `NormalTxType`。
- RWA 交易带非空 `data` 时返回错误。
- 非 RWA 旧格式 JSONL 保持兼容。

### 6.5 分片路由调整

目标：`buy` 和 `sell` 都只发送到源 agent 所在分片。

当前 `../../supervisor/committee/committee.go` 的 `packShardTxs` 接受 `locFunc`，不同 committee 有不同定位逻辑。`static_relay` 已经按 sender 定位，基本符合需求；broker 模式会判断 sender / recipient 所在分片并可能生成 broker 交易，需要处理 RWA 例外。

建议新增统一定位方法：

```go
func sourceShardOnlyLoc(tx transaction.Transaction, shardNum int64) int64 {
	return partition.DefaultAccountLoc(tx.Sender, shardNum)
}
```

处理规则：

- 对 RWA `buy` / `sell`，所有模式都按 `Sender` 定位。
- 对 RWA `buy`，即使 `Recipient` 在其他分片，也不生成 broker 交易，不生成 relay 交易。
- 对 RWA `sell`，`Recipient` 为空地址，不参与目标分片计算。
- 原有 `pay` 和普通转账逻辑不变。

改造点：

- `../../supervisor/committee/staticrelay.go`：确认仍按 sender 分片。
- `../../supervisor/committee/staticbroker.go`：在 broker 判断前识别 RWA 交易，直接加入 `sendTxs`，不调用 broker 拆分。
- `../../supervisor/committee/clparelay.go`：RWA 交易按 sender 当前分片定位，避免 recipient 影响图边更新。
- `../../supervisor/committee/clpabroker.go`：RWA 交易绕过 broker 逻辑，按 sender 分片发送。
- 动态分片图更新时，RWA `buy` 是否加入 sender-recipient 边需要明确。建议 MVP 不加入 CLPA 迁移图，避免 RWA 演示交易改变分片重分配行为。

### 6.6 余额不足也上链

目标：即使 `buy.value` 大于买方账户余额，交易仍然进入区块，并可在实验后被分析。

当前普通转账执行逻辑在 `../../pkg/chain/txexecute.go`，存在余额检查：

```go
if !core.CanTransfer(s, sAddr, uVal) {
	return fmt.Errorf("transfer failed: the balance of %x is not enough", tx.Sender)
}
```

如果 RWA `buy` 仍走普通转账扣款逻辑，余额不足可能导致区块执行失败或交易状态异常。为满足“余额不足也不用管，也上链到区块中”，建议对 RWA 交易采用“记录型普通交易”执行语义：

```text
RWA sell：
  只记录交易，不转账，不改余额。

RWA buy：
  记录交易。
  如果余额足够，可以按普通转账更新 buyer/seller 余额。
  如果余额不足，不返回错误，交易仍视为执行完成。
  是否修改余额需固定规则，建议余额不足时不修改双方余额，并在 RWA 汇总 CSV 中标记 insufficient_balance=true。
```

这样既满足“上链”，又避免出现负余额或状态数据库异常。

如果演示强依赖“买方余额曲线反映支出”，也可以选择不做余额检查、允许扣成负数，但当前账户余额基于 uint256 / big 整数模型，支持负数会引入更大改造，不建议。

推荐规则：

| 场景 | 是否上链 | 是否修改余额 | 是否记录 RWA 事件 |
| --- | --- | --- | --- |
| `sell` | 是 | 否 | 是 |
| `buy` 余额足够 | 是 | 是 | 是 |
| `buy` 余额不足 | 是 | 否 | 是，标记 `insufficient_balance=true` |

需要注意：如果“即使余额不足也不用管”被理解为“不检查也不标记”，则可以省略 `insufficient_balance`；但为了后续分析和监管可视化，建议保留该字段。

### 6.7 区块汇总与 CSV 输出

目标：实验结束后从所有分片区块中汇总 RWA 买卖交易。

复用 `../../agentsupervisor/agentcsv.go` 中已有的区块读取能力：

```text
chainDir/data/boltdb
  -> shard_0_node_0
  -> shard_1_node_0
  -> shard_2_node_0
  -> shard_3_node_0
```

新增文件建议：

```text
agentsupervisor/rwacsv.go
```

新增输出目录和文件：

```text
exp/agentemu-results/round_001/rwa/
  rwa_orders.csv
  rwa_buys.csv
  rwa_sells.csv
  rwa_summary.csv
```

`rwa_orders.csv` 字段建议：

```text
block_height
shard_id
tx_index
block_time_ms
tx_hash
rwa_action
agent_id
target_id
sender
recipient
value
request_id
compute_id
unit_price
quantity
insufficient_balance
```

`rwa_summary.csv` 字段建议：

```text
compute_id
sell_count
buy_count
total_buy_quantity
total_buy_value
min_unit_price
max_unit_price
avg_unit_price
median_unit_price
unique_buyers
unique_sellers
insufficient_balance_count
```

汇总规则：

- 遍历所有分片 leader 区块。
- 只筛选 `tx.IsRWATx()`。
- 按 `block_time`、`shard_id`、`block_height`、`tx_index` 排序。
- `sell` 记录卖方报价和出售份数。
- `buy` 记录购买份数、支付金额、卖方、买方。
- 对 `buy` 的单价可以通过 `value / quantity` 推导，也可以从最近报价关系中补充；建议直接保存 `effective_unit_price = value / quantity`。
- 如果 `quantity == 0`，视为坏数据并跳过或标记错误；正常编译阶段应已禁止。

在 `../../agentsupervisor/loop.go` 的实验结束阶段，继 `WriteAgentCSVs` 后调用新增的 `WriteRWACSVs`。

### 6.8 图表生成

目标：为 RWA 买卖交易生成可视化结果，和现有余额图一起进入 HTML 图册。

新增脚本建议：

```text
figs/python_code/plot_rwa_trades.py
```

输出图片建议：

```text
figs/figs_results/rwa_price_boxplot.png
figs/figs_results/rwa_quantity_boxplot.png
figs/figs_results/rwa_value_by_compute.png
figs/figs_results/rwa_agent_buy_sell_counts.png
figs/figs_results/rwa_insufficient_balance.png
```

图表说明：

| 图 | 含义 |
| --- | --- |
| RWA 成交单价箱线图 | 按 `compute_id` 展示买入单价分布 |
| RWA 购买份数箱线图 | 按 `compute_id` 展示购买份数分布 |
| RWA 成交金额柱状图 | 按 `compute_id` 汇总购买金额 |
| Agent 买卖次数图 | 展示每个 agent 的买入/卖出次数 |
| 余额不足交易图 | 展示余额不足但仍上链的 RWA buy 数量 |

改造 `../../run_agentemu.sh`：

```text
python3 plot_agent_balance.py --data-dir round_001/agents --fig-dir figs_results
python3 plot_rwa_trades.py --rwa-dir round_001/rwa --fig-dir figs_results
python3 build_fig_html.py --fig-dir figs_results --data-dir round_001/agents --rwa-dir round_001/rwa
```

改造 `../../figs/python_code/build_fig_html.py`，新增 RWA 图表分组：

```text
Agent Balance Figures
RWA Trading Figures
```

### 6.9 文档与示例 trace

新增或更新文档：

- `../../README_zh.md`
- `../../README.md`
- `../../AGENTS.md`

新增示例 trace：

```text
traces/rwa_compute_demo.jsonl
```

示例内容：

```jsonl
{"agent_id":"provider-001","action":"join","ts":1,"request_id":"join-provider-001"}
{"agent_id":"buyer-001","action":"join","ts":2,"request_id":"join-buyer-001"}
{"agent_id":"provider-001","action":"sell","compute_id":"gpu-a100-hour","unit_price":"500000","quantity":100,"ts":3,"request_id":"sell-gpu-a100-001"}
{"agent_id":"buyer-001","action":"buy","target":"provider-001","compute_id":"gpu-a100-hour","quantity":10,"ts":4,"request_id":"buy-gpu-a100-001"}
```

文档需要明确：

- `buy` 不指定出价。
- `buy.value` 由对应 `sell.unit_price * buy.quantity` 计算。
- `sell` 只是链上声明，不是订单簿。
- 不做库存扣减。
- 不做余额准入校验。
- 不部署智能合约。
- RWA 图表来自实验后区块汇总。

## 7. 测试方案

### 7.1 单元测试

Trace 解析测试：

- `sell` 字段完整时解析成功。
- `buy` 字段完整时解析成功。
- `sell.quantity=0` 报错。
- `buy.quantity=0` 报错。
- `sell.unit_price` 非整数报错。
- `buy` 缺少 `target` 报错。
- `buy` 缺少 `compute_id` 报错。

编译测试：

- `sell` 编译为 `NormalTxType`。
- `sell.Recipient == account.EmptyAccountAddr`。
- `sell.Value == 0`。
- `sell.Data` 为空。
- `sell.RWATxOpt` 字段完整。
- `buy` 编译为 `NormalTxType`。
- `buy.Value == unit_price * quantity`。
- `buy.Recipient` 为卖方 DID 地址。
- `buy.Data` 为空。
- `buy` 不检查买方余额。
- `buy` 找不到卖方报价时报错。

交易源测试：

- `TraceSourceJSONL` 能读取 RWA `sell`。
- `TraceSourceJSONL` 能读取 RWA `buy`。
- 空 `recipient` / 空 `value` 只允许出现在 RWA `sell`。
- RWA 交易带非空 `data` 报错。
- 老的普通交易 JSONL 兼容。

分片路由测试：

- RWA `sell` 被发送到 sender 分片。
- RWA `buy` 即使 buyer 和 seller 不同分片，也只发送到 buyer 分片。
- broker 模式下 RWA `buy` 不生成 broker 交易。
- relay 模式下 RWA `buy` 不生成跨片 relay 交易。

区块汇总测试：

- 多分片区块中都存在 RWA 交易时，能全部汇总。
- `rwa_orders.csv` 行数等于链上 RWA 交易数。
- `rwa_summary.csv` 聚合值正确。
- 余额不足的 `buy` 仍出现在 RWA CSV 中。

### 7.2 集成测试

推荐新增集成用例：

```text
go test ./agentsupervisor -run TestRWA
go test ./supervisor/txsource/tracesource -run TestRWA
go test ./supervisor/committee -run TestRWA
```

全量验证：

```bash
go build ./...
go test -gcflags=all='-N -l' ./...
go mod tidy
golangci-lint run ./... --fix
bash run_agentemu.sh
```

运行后检查：

```text
exp/agentemu-results/round_001/rwa/rwa_orders.csv
exp/agentemu-results/round_001/rwa/rwa_summary.csv
figs/figs_results/rwa_price_boxplot.png
figs/figs_results/index.html
```

## 8. 风险与注意事项

第一，不能把 RWA 元数据放入 `tx.Data`。当前 `TxType()` 会把非空 `Data` 识别为合约交易，导致进入合约部署或调用路径，与“不涉及智能合约”冲突。

第二，`sell` 的 `to` 和 `value` 在 JSONL 中可以为空，但链内 `Transaction` 结构仍应归一化为 `Recipient = EmptyAccountAddr`、`Value = 0`，避免 `nil` 指针和解析分支过多。

第三，`buy` 只发送源 agent 分片后，卖方如果在其他分片，卖方余额不会天然在本分片正确更新。由于本期目标是“普通交易上链记录和实验后分析”，建议将 RWA `buy` 视为记录型普通交易；余额足够时是否更新卖方余额需要谨慎。如果要严格维护跨分片余额，就会重新引入跨分片交易机制，与“只发源分片”冲突。

第四，余额不足仍上链会改变当前普通转账执行语义。建议只对 RWA `buy` 特判，不影响 `pay` 和普通转账，否则会破坏现有实验语义。

第五，broker 和 CLPA 模式可能自动基于 sender / recipient 生成跨片处理。RWA 交易必须显式绕过这些逻辑，否则无法满足“只发送到源 agent 所在分片”。

第六，RWA 分析应从区块存储读取，而不是从 `agent_transactions.jsonl` 读取。这样才能保证图表展示的是已经上链的事实。

## 9. 验收标准

功能验收：

- trace 支持 `buy` 和 `sell`。
- `sell` 可以不指定 `to` 和 `value`。
- `buy` 不指定出价，自动按卖方报价计算 `value`。
- `buy` 和 `sell` 均为 `NormalTxType`。
- `buy` 和 `sell` 均不触发合约部署或合约调用。
- 买方余额不足时，`buy` 仍能进入区块。
- RWA `buy` 和 `sell` 只发送到源 agent 所在分片。
- 实验结束后可以从所有分片区块中汇总 RWA 交易。
- 能生成 RWA CSV 和图表。
- 原有 `join`、`leave`、`pay`、plain transfer 行为不受影响。

数据验收：

- `rwa_orders.csv` 能看到所有链上 RWA 买卖交易。
- `rwa_summary.csv` 能按 `compute_id` 聚合数量、价格、金额。
- 箱线图能展示不同 `compute_id` 的价格或购买份数分布。
- HTML 图册能同时展示原有余额图和新增 RWA 图。

工程验收：

- `go build ./...` 通过。
- `go test -gcflags=all='-N -l' ./...` 通过。
- `go mod tidy` 无 diff。
- `golangci-lint run ./... --fix` 后无 lint 错误。
- `bash run_agentemu.sh` 可完成默认或 RWA demo trace 的完整实验流程。

## 10. 推荐实施顺序

建议按以下顺序开发，降低联调风险：

1. 扩展 `Transaction` RWA 元数据字段，保证 RWA 交易仍是 `NormalTxType`。
2. 扩展 trace `buy` / `sell` 解析和校验。
3. 扩展 `AgentSupervisor` 编译逻辑，实现卖价引用和 value 计算。
4. 扩展 `agent_transactions.jsonl` 写入和 `TraceSourceJSONL` 读取。
5. 调整 supervisor committee 路由，保证 RWA 只按 sender 分片发送。
6. 调整链执行逻辑，保证 RWA `buy` 余额不足仍上链。
7. 新增 RWA 区块汇总 CSV。
8. 新增 RWA 绘图脚本和 HTML 图册入口。
9. 补充 README、示例 trace 和测试用例。
10. 执行完整构建、测试、lint 和 `../../run_agentemu.sh` 验证。
