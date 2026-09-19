package agentsupervisor

import (
	"context"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/HuangLab-SYSU/block-emulator-x/config"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	coreblock "github.com/HuangLab-SYSU/block-emulator-x/pkg/core/block"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	blockstore "github.com/HuangLab-SYSU/block-emulator-x/pkg/storage/block"
)

// AgentsDirName hosts the per-agent CSVs inside a round's output directory.
const AgentsDirName = "agents"

var agentCSVHeader = []string{
	"block_height",
	"tx_hash",
	"sender",
	"recipient",
	"value",
	"balance",
	"block_time_ms",
}

// agentTxRow is one committed transaction seen from one agent's perspective.
type agentTxRow struct {
	blockHeight uint64
	txHash      string
	sender      string
	recipient   string
	value       string
	balance     string
	blockTimeMs int64
}

// WriteAgentCSVs reads the committed blocks of a finished chain run and writes
// one CSV per agent into outDir, holding every on-chain transaction the agent
// took part in (as sender or recipient) with the running account balance. The
// data source is the shards' block storages, so this is equivalent to
// recording every block right after it committed, without touching any
// platform code.
func WriteAgentCSVs(ctx context.Context, chainDir string, shardNum int64, reg *Registry, outDir string) error {
	agents := make(map[account.Address]string, len(reg.agents))
	ids := make([]string, 0, len(reg.agents))

	for id, agent := range reg.agents {
		addr, err := didAddress(agent.DID)
		if err != nil {
			return fmt.Errorf("resolve agent %s address: %w", id, err)
		}

		agents[addr] = agent.AgentID
		ids = append(ids, agent.AgentID)
	}

	boltDir := filepath.Join(chainDir, "data", "boltdb")

	blocks := make([][]*coreblock.Block, shardNum)
	for shard := int64(0); shard < shardNum; shard++ {
		bs, err := readShardBlocks(ctx, boltDir, shard)
		if err != nil {
			return err
		}

		blocks[shard] = bs
	}

	rows := buildAgentRows(blocks, agents)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create agents dir: %w", err)
	}

	sort.Strings(ids)

	for _, id := range ids {
		agentRows, ok := rows[id]
		if !ok {
			continue
		}

		if err := writeAgentCSV(filepath.Join(outDir, agentFileName(id)), agentRows); err != nil {
			return err
		}
	}

	return nil
}

// readShardBlocks walks the leader's block store (shard_S_node_0) backwards
// from the newest block hash to the genesis block and returns the blocks in
// ascending height order.
func readShardBlocks(ctx context.Context, boltDir string, shard int64) ([]*coreblock.Block, error) {
	store, err := blockstore.NewBoltStore(
		config.BoltCfg{FilePathDir: boltDir},
		config.LocalParams{ShardID: shard, NodeID: 0},
	)
	if err != nil {
		return nil, fmt.Errorf("open block store of shard %d: %w", shard, err)
	}

	defer func() { _ = store.Close() }()

	newest, err := store.GetNewestBlockHash(ctx)
	if err != nil {
		return nil, fmt.Errorf("read newest block hash of shard %d: %w", shard, err)
	}

	var chain []*coreblock.Block

	for hash := newest; len(hash) > 0; {
		raw, err := store.GetBlockByHash(ctx, hash)
		if err != nil {
			// The genesis block's ParentBlockHash is the hash of the
			// zero-value header, so nothing is stored below it; walking past
			// genesis simply ends the chain. Any other gap is real corruption.
			if len(chain) > 0 && chain[len(chain)-1].Number <= 1 {
				break
			}

			return nil, fmt.Errorf("read block %x of shard %d: %w", hash, shard, err)
		}

		b, err := coreblock.DecodeBlock(raw)
		if err != nil {
			return nil, fmt.Errorf("decode block of shard %d: %w", shard, err)
		}

		chain = append(chain, b)
		hash = b.ParentBlockHash
	}

	// The walk went newest -> genesis; flip it.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	return chain, nil
}

// buildAgentRows turns the shards' committed blocks into per-agent row lists.
// Transactions are processed in one deterministic global pass, ordered by the
// commit time of the block that packaged them (Header.CreateTime, i.e. the
// on-chain time), then by (shard, height, in-block index) as tie-breakers for
// blocks stamped within the same instant, so balances accumulate reproducibly
// in the order the chain actually executed the transactions. An account is
// lazily initialized to NormalInitBalance on its first appearance, exactly
// like the chain does.
func buildAgentRows(perShard [][]*coreblock.Block, agents map[account.Address]string) map[string][]agentTxRow {
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
				refs = append(refs, txRef{
					blockTime: b.Header.CreateTime,
					shard:     shard,
					height:    b.Number,
					index:     i,
					tx:        b.TxList[i],
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

	init, _ := new(big.Int).SetString(account.NormalInitBalanceStr, 10)
	balances := make(map[account.Address]*big.Int)
	rows := make(map[string][]agentTxRow)

	touch := func(addr account.Address) *big.Int {
		if b, ok := balances[addr]; ok {
			return b
		}

		b := new(big.Int).Set(init)
		balances[addr] = b

		return b
	}

	for _, ref := range refs {
		tx := ref.tx
		senderID, senderIsAgent := agents[tx.Sender]
		recipientID, recipientIsAgent := agents[tx.Recipient]

		// A cross-shard transfer executes as relay1 (debit, sender's shard)
		// and relay2 (credit, recipient's shard); each side is recorded once,
		// from the block that actually executed it, at that block's commit
		// time.
		if senderIsAgent && tx.RelayStage != transaction.Relay2Tx {
			balance := touch(tx.Sender)
			balance.Sub(balance, tx.Value)
			rows[senderID] = append(rows[senderID], agentTxRow{
				blockHeight: ref.height,
				txHash:      txHexHash(tx),
				sender:      fmt.Sprintf("%x", tx.Sender),
				recipient:   fmt.Sprintf("%x", tx.Recipient),
				value:       tx.Value.String(),
				balance:     balance.String(),
				blockTimeMs: ref.blockTime.UnixMilli(),
			})
		}

		if recipientIsAgent && (tx.RelayStage == transaction.Relay2Tx || tx.RelayStage == 0) {
			balance := touch(tx.Recipient)
			balance.Add(balance, tx.Value)
			rows[recipientID] = append(rows[recipientID], agentTxRow{
				blockHeight: ref.height,
				txHash:      txHexHash(tx),
				sender:      fmt.Sprintf("%x", tx.Sender),
				recipient:   fmt.Sprintf("%x", tx.Recipient),
				value:       tx.Value.String(),
				balance:     balance.String(),
				blockTimeMs: ref.blockTime.UnixMilli(),
			})
		}
	}

	return rows
}

// txHexHash reports the hash that identifies the LOGICAL transaction: for
// cross-shard relay legs the blocks store the split Relay1/Relay2
// transactions whose own hashes differ from the original, so the original
// hash (ROriginalHash) is used instead — it is the very hash recorded in
// relay_stats_detail_tx_info.csv and agent_action_txs.jsonl, keeping the
// per-agent rows joinable with both.
func txHexHash(tx transaction.Transaction) string {
	if len(tx.ROriginalHash) != 0 {
		return hex.EncodeToString(tx.ROriginalHash)
	}

	hash, err := tx.Hash()
	if err != nil {
		return ""
	}

	return hex.EncodeToString(hash)
}

func writeAgentCSV(path string, rows []agentTxRow) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create agent csv: %w", err)
	}

	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)

	if err := w.Write(agentCSVHeader); err != nil {
		return fmt.Errorf("write agent csv header: %w", err)
	}

	for _, row := range rows {
		record := []string{
			fmt.Sprintf("%d", row.blockHeight),
			row.txHash,
			row.sender,
			row.recipient,
			row.value,
			row.balance,
			fmt.Sprintf("%d", row.blockTimeMs),
		}
		if err := w.Write(record); err != nil {
			return fmt.Errorf("write agent csv row: %w", err)
		}
	}

	w.Flush()

	return w.Error()
}

// agentFileName maps an agent id to a safe file name.
func agentFileName(agentID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, agentID) + ".csv"
}
