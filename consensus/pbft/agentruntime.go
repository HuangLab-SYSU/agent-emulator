package pbft

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/hex"
	"fmt"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/message"
)

func (n *Node) sendRuntimeMessage(ctx context.Context, value any) error {
	dest, err := n.resolver.GetSupervisor()
	if err != nil {
		return fmt.Errorf("resolve supervisor: %w", err)
	}

	msg, err := message.WrapMsg(value)
	if err != nil {
		return fmt.Errorf("wrap runtime message: %w", err)
	}

	n.conn.SendMsg2Dest(ctx, dest, msg)

	return nil
}

func (n *Node) handlePermitGrant(_ context.Context, payload []byte) error {
	if n.pbftMeta.cfg.AgentRuntimeConfig == "" {
		return nil
	}

	var grant message.ProposalPermitGrant
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&grant); err != nil {
		return fmt.Errorf("decode permit: %w", err)
	}

	if n.permitRequested && grant.ID == n.permitID {
		n.permitGranted = true
	}

	return nil
}

// Runs on the existing serial node loop; GetAccountStates also takes Chain.mux.
func (n *Node) handleAccountSnapshot(ctx context.Context, payload []byte) error {
	if n.pbftMeta.cfg.AgentRuntimeConfig == "" {
		return nil
	}

	var req message.AccountSnapshotRequest
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&req); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}

	states, err := n.bc.GetAccountStates(ctx, req.Addresses)

	response := &message.AccountSnapshotResponse{
		ID:      req.ID,
		ShardID: n.pbftMeta.lp.ShardID,
		Height:  n.bc.GetCurHeader().Number,
		States:  states,
	}
	if err != nil {
		response.Error = err.Error()
	} else {
		response.Block, err = n.latestRuntimeBlock(ctx)
		if err != nil {
			response.Error = err.Error()
		}
	}

	return n.sendRuntimeMessage(ctx, response)
}

// Bound model context independently of the configured maximum block size.
func (n *Node) latestRuntimeBlock(ctx context.Context) (message.SnapshotBlock, error) {
	const transactionPreviewLimit = 32

	blocks, err := n.bc.GetBlocksAfterHeight(ctx, int64(n.bc.GetCurHeader().Number))
	if err != nil {
		return message.SnapshotBlock{}, fmt.Errorf("read latest block: %w", err)
	}

	if len(blocks) != 1 {
		return message.SnapshotBlock{}, fmt.Errorf("latest block unavailable")
	}

	b := blocks[0]

	hash, err := b.Hash()
	if err != nil {
		return message.SnapshotBlock{}, fmt.Errorf("hash latest block: %w", err)
	}

	result := message.SnapshotBlock{
		Height:                b.Number,
		Hash:                  hex.EncodeToString(hash),
		ParentHash:            hex.EncodeToString(b.ParentBlockHash),
		CreatedAt:             b.CreateTime,
		TransactionCount:      len(b.TxList),
		TransactionsTruncated: len(b.TxList) > transactionPreviewLimit,
	}
	for _, tx := range b.TxList[:min(len(b.TxList), transactionPreviewLimit)] {
		hash, err := tx.Hash()
		if err != nil {
			return result, fmt.Errorf("hash snapshot transaction: %w", err)
		}

		result.Transactions = append(
			result.Transactions,
			message.SnapshotTransaction{
				Hash:         hex.EncodeToString(hash),
				OriginalHash: hex.EncodeToString(tx.ROriginalHash),
				Sender:       fmt.Sprintf("0x%x", tx.Sender),
				Recipient:    fmt.Sprintf("0x%x", tx.Recipient),
				Value:        tx.Value.String(),
				RelayStage:   tx.RelayStage,
			},
		)
	}

	return result, nil
}
