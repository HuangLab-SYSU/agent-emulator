package agentsupervisor

import (
	"context"
	"encoding/csv"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	coreblock "github.com/HuangLab-SYSU/block-emulator-x/pkg/core/block"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
)

const RWADirName = "rwa"

var rwaOrdersHeader = []string{
	"block_height",
	"shard_id",
	"tx_index",
	"block_time_ms",
	"tx_hash",
	"rwa_action",
	"agent_id",
	"target_id",
	"sender",
	"recipient",
	"value",
	"request_id",
	"compute_id",
	"unit_price",
	"quantity",
	"insufficient_balance",
}

var rwaSummaryHeader = []string{
	"compute_id",
	"sell_count",
	"buy_count",
	"total_buy_quantity",
	"total_buy_value",
	"min_unit_price",
	"max_unit_price",
	"avg_unit_price",
	"median_unit_price",
	"unique_buyers",
	"unique_sellers",
	"insufficient_balance_count",
}

type rwaTxRow struct {
	blockHeight         uint64
	shardID             int
	txIndex             int
	blockTimeMs         int64
	txHash              string
	rwaAction           string
	agentID             string
	targetID            string
	sender              string
	recipient           string
	value               string
	requestID           string
	computeID           string
	unitPrice           string
	quantity            uint64
	insufficientBalance bool
}

type rwaSummaryRow struct {
	computeID                string
	sellCount                int
	buyCount                 int
	totalBuyQuantity         uint64
	totalBuyValue            *big.Int
	unitPrices               []*big.Int
	buyers                   map[string]struct{}
	sellers                  map[string]struct{}
	insufficientBalanceCount int
}

// WriteRWACSVs reads committed blocks of a finished chain run, extracts all RWA
// buy/sell transactions, and writes order-level and summary CSVs into outDir.
func WriteRWACSVs(ctx context.Context, chainDir string, shardNum int64, outDir string) error {
	boltDir := filepath.Join(chainDir, "data", "boltdb")

	blocks := make([][]*coreblock.Block, shardNum)
	for shard := int64(0); shard < shardNum; shard++ {
		bs, err := readShardBlocks(ctx, boltDir, shard)
		if err != nil {
			return err
		}

		blocks[shard] = bs
	}

	rows := buildRWARows(blocks)
	if len(rows) == 0 {
		return nil
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create rwa dir: %w", err)
	}

	if err := writeRWAOrdersCSV(filepath.Join(outDir, "rwa_orders.csv"), rows); err != nil {
		return err
	}
	if err := writeRWAOrdersCSV(filepath.Join(outDir, "rwa_buys.csv"), filterRWARows(rows, transaction.RWAActionBuy)); err != nil {
		return err
	}
	if err := writeRWAOrdersCSV(filepath.Join(outDir, "rwa_sells.csv"), filterRWARows(rows, transaction.RWAActionSell)); err != nil {
		return err
	}
	if err := writeRWASummaryCSV(filepath.Join(outDir, "rwa_summary.csv"), buildRWASummary(rows)); err != nil {
		return err
	}

	return nil
}

func buildRWARows(perShard [][]*coreblock.Block) []rwaTxRow {
	type txRef struct {
		blockTime time.Time
		shard     int
		height    uint64
		index     int
		tx        transaction.Transaction
	}

	var refs []txRef
	for shard, blocks := range perShard {
		for _, b := range blocks {
			for i := range b.TxList {
				tx := b.TxList[i]
				if !tx.IsRWATx() {
					continue
				}

				refs = append(refs, txRef{
					blockTime: b.CreateTime,
					shard:     shard,
					height:    b.Number,
					index:     i,
					tx:        tx,
				})
			}
		}
	}

	sort.SliceStable(refs, func(i, j int) bool {
		ti, tj := refs[i].blockTime, refs[j].blockTime
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if refs[i].shard != refs[j].shard {
			return refs[i].shard < refs[j].shard
		}
		if refs[i].height != refs[j].height {
			return refs[i].height < refs[j].height
		}
		return refs[i].index < refs[j].index
	})

	balances := make(map[account.Address]*big.Int)
	initBalance, _ := new(big.Int).SetString(account.NormalInitBalanceStr, 10)
	touch := func(addr account.Address) *big.Int {
		if addr == account.EmptyAccountAddr {
			return big.NewInt(0)
		}
		if b, ok := balances[addr]; ok {
			return b
		}

		b := new(big.Int).Set(initBalance)
		balances[addr] = b

		return b
	}

	rows := make([]rwaTxRow, 0, len(refs))
	for _, ref := range refs {
		tx := ref.tx
		insufficient := false
		if tx.IsRWABuy() {
			balance := touch(tx.Sender)
			if balance.Cmp(tx.Value) >= 0 {
				balance.Sub(balance, tx.Value)
				touch(tx.Recipient).Add(touch(tx.Recipient), tx.Value)
			} else {
				insufficient = true
			}
		} else {
			touch(tx.Sender)
		}

		unitPrice := ""
		if tx.UnitPrice != nil {
			unitPrice = tx.UnitPrice.String()
		} else if tx.IsRWABuy() && tx.Quantity > 0 {
			unitPrice = new(big.Int).Div(new(big.Int).Set(tx.Value), new(big.Int).SetUint64(tx.Quantity)).String()
		}

		rows = append(rows, rwaTxRow{
			blockHeight:         ref.height,
			shardID:             ref.shard,
			txIndex:             ref.index,
			blockTimeMs:         ref.blockTime.UnixMilli(),
			txHash:              txHexHash(tx),
			rwaAction:           tx.RWAAction,
			agentID:             tx.AgentID,
			targetID:            tx.TargetID,
			sender:              fmt.Sprintf("%x", tx.Sender),
			recipient:           formatRWARecipient(tx),
			value:               tx.Value.String(),
			requestID:           tx.RequestID,
			computeID:           tx.ComputeID,
			unitPrice:           unitPrice,
			quantity:            tx.Quantity,
			insufficientBalance: insufficient,
		})
	}

	return rows
}

func formatRWARecipient(tx transaction.Transaction) string {
	if tx.IsRWASell() {
		return ""
	}

	return fmt.Sprintf("%x", tx.Recipient)
}

func filterRWARows(rows []rwaTxRow, action string) []rwaTxRow {
	filtered := make([]rwaTxRow, 0, len(rows))
	for _, row := range rows {
		if row.rwaAction == action {
			filtered = append(filtered, row)
		}
	}

	return filtered
}

func writeRWAOrdersCSV(path string, rows []rwaTxRow) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create rwa csv: %w", err)
	}

	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	if err := w.Write(rwaOrdersHeader); err != nil {
		return fmt.Errorf("write rwa csv header: %w", err)
	}

	for _, row := range rows {
		record := []string{
			fmt.Sprintf("%d", row.blockHeight),
			fmt.Sprintf("%d", row.shardID),
			fmt.Sprintf("%d", row.txIndex),
			fmt.Sprintf("%d", row.blockTimeMs),
			row.txHash,
			row.rwaAction,
			row.agentID,
			row.targetID,
			row.sender,
			row.recipient,
			row.value,
			row.requestID,
			row.computeID,
			row.unitPrice,
			fmt.Sprintf("%d", row.quantity),
			strconv.FormatBool(row.insufficientBalance),
		}
		if err := w.Write(record); err != nil {
			return fmt.Errorf("write rwa csv row: %w", err)
		}
	}

	w.Flush()

	return w.Error()
}

func buildRWASummary(rows []rwaTxRow) []rwaSummaryRow {
	byCompute := make(map[string]*rwaSummaryRow)
	get := func(computeID string) *rwaSummaryRow {
		row, ok := byCompute[computeID]
		if ok {
			return row
		}

		row = &rwaSummaryRow{
			computeID:     computeID,
			totalBuyValue: big.NewInt(0),
			buyers:        make(map[string]struct{}),
			sellers:       make(map[string]struct{}),
		}
		byCompute[computeID] = row

		return row
	}

	for _, row := range rows {
		summary := get(row.computeID)
		switch row.rwaAction {
		case transaction.RWAActionSell:
			summary.sellCount++
			if row.agentID != "" {
				summary.sellers[row.agentID] = struct{}{}
			}
			if price, ok := new(big.Int).SetString(row.unitPrice, 10); ok {
				summary.unitPrices = append(summary.unitPrices, price)
			}
		case transaction.RWAActionBuy:
			summary.buyCount++
			summary.totalBuyQuantity += row.quantity
			if row.agentID != "" {
				summary.buyers[row.agentID] = struct{}{}
			}
			if value, ok := new(big.Int).SetString(row.value, 10); ok {
				summary.totalBuyValue.Add(summary.totalBuyValue, value)
			}
			if price, ok := new(big.Int).SetString(row.unitPrice, 10); ok {
				summary.unitPrices = append(summary.unitPrices, price)
			}
			if row.insufficientBalance {
				summary.insufficientBalanceCount++
			}
		}
	}

	out := make([]rwaSummaryRow, 0, len(byCompute))
	for _, row := range byCompute {
		out = append(out, *row)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].computeID < out[j].computeID })

	return out
}

func writeRWASummaryCSV(path string, rows []rwaSummaryRow) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create rwa summary csv: %w", err)
	}

	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	if err := w.Write(rwaSummaryHeader); err != nil {
		return fmt.Errorf("write rwa summary header: %w", err)
	}

	for _, row := range rows {
		minPrice, maxPrice, avgPrice, medianPrice := summarizePrices(row.unitPrices)
		record := []string{
			row.computeID,
			fmt.Sprintf("%d", row.sellCount),
			fmt.Sprintf("%d", row.buyCount),
			fmt.Sprintf("%d", row.totalBuyQuantity),
			row.totalBuyValue.String(),
			minPrice,
			maxPrice,
			avgPrice,
			medianPrice,
			fmt.Sprintf("%d", len(row.buyers)),
			fmt.Sprintf("%d", len(row.sellers)),
			fmt.Sprintf("%d", row.insufficientBalanceCount),
		}
		if err := w.Write(record); err != nil {
			return fmt.Errorf("write rwa summary row: %w", err)
		}
	}

	w.Flush()

	return w.Error()
}

func summarizePrices(prices []*big.Int) (string, string, string, string) {
	if len(prices) == 0 {
		return "", "", "", ""
	}

	sorted := make([]*big.Int, len(prices))
	for i, price := range prices {
		sorted[i] = new(big.Int).Set(price)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Cmp(sorted[j]) < 0 })

	sum := big.NewInt(0)
	for _, price := range sorted {
		sum.Add(sum, price)
	}

	avg := new(big.Int).Div(sum, new(big.Int).SetInt64(int64(len(sorted))))
	median := new(big.Int).Set(sorted[len(sorted)/2])
	if len(sorted)%2 == 0 {
		median.Add(sorted[len(sorted)/2-1], sorted[len(sorted)/2])
		median.Div(median, big.NewInt(2))
	}

	return sorted[0].String(), sorted[len(sorted)-1].String(), avg.String(), median.String()
}
