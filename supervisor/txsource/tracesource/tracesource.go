package tracesource

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/utils"
)

const Key = "trace_source_JSONL"

// traceLine mirrors the JSONL layout written by agentsupervisor's WriteResult, so a
// generated trace can be replayed by the supervisor without any intermediate
// conversion.
type traceLine struct {
	Hash      string `json:"hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Value     string `json:"value"`
	Nonce     uint64 `json:"nonce"`
	Data      string `json:"data"`
	RWAAction string `json:"rwa_action"`
	AgentID   string `json:"agent_id"`
	TargetID  string `json:"target_id"`
	RequestID string `json:"request_id"`
	ComputeID string `json:"compute_id"`
	UnitPrice string `json:"unit_price"`
	Quantity  uint64 `json:"quantity"`
}

// TraceSourceJSONL implements TxSource by reading a JSONL trace file
// (agent_transactions.jsonl, one JSON object per line).
type TraceSourceJSONL struct {
	file    *os.File
	scanner *bufio.Scanner
	done    bool
	line    int
}

func NewTraceSourceJSONL(filename string) (*TraceSourceJSONL, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open trace file: %w", err)
	}

	return &TraceSourceJSONL{
		file:    f,
		scanner: bufio.NewScanner(f),
	}, nil
}

func (ts *TraceSourceJSONL) ReadTxs(size int64) ([]transaction.Transaction, error) {
	if ts.done {
		return nil, nil
	}

	ret := make([]transaction.Transaction, 0, size)

	for int64(len(ret)) < size {
		if !ts.scanner.Scan() {
			ts.close()

			if err := ts.scanner.Err(); err != nil {
				return nil, fmt.Errorf("failed to read trace file: %w", err)
			}

			break
		}

		ts.line++

		tx, err := ts.line2Tx(ts.scanner.Bytes())
		if err != nil {
			ts.close()

			return nil, fmt.Errorf("decode trace line %d: %w", ts.line, err)
		}

		ret = append(ret, *tx)
	}

	return ret, nil
}

func (ts *TraceSourceJSONL) close() {
	ts.done = true
	_ = ts.file.Close()
}

func (ts *TraceSourceJSONL) line2Tx(line []byte) (*transaction.Transaction, error) {
	var tl traceLine
	if err := json.Unmarshal(line, &tl); err != nil {
		return nil, fmt.Errorf("unmarshal trace line: %w", err)
	}

	sender, err := utils.Hex2Addr(tl.Sender)
	if err != nil {
		return nil, fmt.Errorf("parse sender address: %w", err)
	}

	recipient, err := parseRecipient(tl)
	if err != nil {
		return nil, err
	}

	value, err := parseValue(tl)
	if err != nil {
		return nil, err
	}

	tx := transaction.NewTransaction(sender, recipient, value, big.NewInt(0), tl.Nonce, time.Now())

	data, err := utils.Hex2Bytes(tl.Data)
	if err != nil {
		return nil, fmt.Errorf("parse tx data: %w", err)
	}

	tx.Data = data
	if tl.RWAAction != "" {
		if len(data) != 0 {
			return nil, fmt.Errorf("rwa transaction data must be empty")
		}

		if err := fillRWATxOpt(tx, tl); err != nil {
			return nil, err
		}
	}

	return tx, nil
}

func parseRecipient(tl traceLine) (account.Address, error) {
	if tl.Recipient == "" {
		if tl.RWAAction == transaction.RWAActionSell {
			return account.EmptyAccountAddr, nil
		}

		return account.Address{}, fmt.Errorf("recipient is required")
	}

	recipient, err := utils.Hex2Addr(tl.Recipient)
	if err != nil {
		return account.Address{}, fmt.Errorf("parse recipient address: %w", err)
	}

	return recipient, nil
}

func parseValue(tl traceLine) (*big.Int, error) {
	if tl.Value == "" {
		if tl.RWAAction == transaction.RWAActionSell {
			return big.NewInt(0), nil
		}

		return nil, fmt.Errorf("value is required")
	}

	value := new(big.Int)
	if _, ok := value.SetString(tl.Value, 10); !ok {
		return nil, fmt.Errorf("parse value %q", tl.Value)
	}

	return value, nil
}

func fillRWATxOpt(tx *transaction.Transaction, tl traceLine) error {
	switch tl.RWAAction {
	case transaction.RWAActionSell:
		if tl.ComputeID == "" || tl.UnitPrice == "" || tl.Quantity == 0 {
			return fmt.Errorf("sell rwa transaction requires compute_id, unit_price and positive quantity")
		}

		unitPrice, ok := new(big.Int).SetString(tl.UnitPrice, 10)
		if !ok || unitPrice.Sign() < 0 {
			return fmt.Errorf("parse unit_price %q", tl.UnitPrice)
		}

		tx.RWATxOpt = transaction.RWATxOpt{
			RWAAction: tl.RWAAction,
			AgentID:   tl.AgentID,
			RequestID: tl.RequestID,
			ComputeID: tl.ComputeID,
			UnitPrice: unitPrice,
			Quantity:  tl.Quantity,
		}
	case transaction.RWAActionBuy:
		if tl.ComputeID == "" || tl.Quantity == 0 || tl.Recipient == "" || tl.Value == "" {
			return fmt.Errorf("buy rwa transaction requires recipient, value, compute_id and positive quantity")
		}

		tx.RWATxOpt = transaction.RWATxOpt{
			RWAAction: tl.RWAAction,
			AgentID:   tl.AgentID,
			TargetID:  tl.TargetID,
			RequestID: tl.RequestID,
			ComputeID: tl.ComputeID,
			Quantity:  tl.Quantity,
		}
	default:
		return fmt.Errorf("unsupported rwa_action %q", tl.RWAAction)
	}

	return nil
}
