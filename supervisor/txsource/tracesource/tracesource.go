package tracesource

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"time"

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

	recipient, err := utils.Hex2Addr(tl.Recipient)
	if err != nil {
		return nil, fmt.Errorf("parse recipient address: %w", err)
	}

	value := new(big.Int)
	if _, ok := value.SetString(tl.Value, 10); !ok {
		return nil, fmt.Errorf("parse value %q", tl.Value)
	}

	tx := transaction.NewTransaction(sender, recipient, value, big.NewInt(0), tl.Nonce, time.Now())

	data, err := utils.Hex2Bytes(tl.Data)
	if err != nil {
		return nil, fmt.Errorf("parse tx data: %w", err)
	}

	tx.Data = data

	return tx, nil
}
