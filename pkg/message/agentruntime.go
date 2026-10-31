package message

import (
	"time"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
)

const (
	ProposalPermitRequestType   = "ProposalPermitRequest"
	ProposalPermitGrantType     = "ProposalPermitGrant"
	ProposalPermitDoneType      = "ProposalPermitDone"
	AccountSnapshotRequestType  = "AccountSnapshotRequest"
	AccountSnapshotResponseType = "AccountSnapshotResponse"
)

// A permit reserves one proposal, including empty blocks and in-flight consensus.
// Leaders request at most one permit at a time; the supervisor allocates globally.
type ProposalPermitRequest struct {
	ShardID int64
	ID      uint64
}
type (
	ProposalPermitGrant struct{ ID uint64 }
	ProposalPermitDone  struct {
		ShardID int64
		ID      uint64
	}
)

type AccountSnapshotRequest struct {
	ID        uint64
	Addresses []account.Address
}
type SnapshotTransaction struct {
	Hash         string `json:"hash"`
	OriginalHash string `json:"original_hash,omitempty"`
	Sender       string `json:"sender"`
	Recipient    string `json:"recipient"`
	Value        string `json:"value"`
	RelayStage   uint   `json:"relay_stage"`
}

type SnapshotBlock struct {
	Height                uint64                `json:"height"`
	Hash                  string                `json:"hash"`
	ParentHash            string                `json:"parent_hash"`
	CreatedAt             time.Time             `json:"created_at"`
	TransactionCount      int                   `json:"transaction_count"`
	Transactions          []SnapshotTransaction `json:"transactions"`
	TransactionsTruncated bool                  `json:"transactions_truncated"`
}

type AccountSnapshotResponse struct {
	Block   SnapshotBlock
	ID      uint64
	ShardID int64
	Height  uint64
	States  []*account.State
	Error   string
}
