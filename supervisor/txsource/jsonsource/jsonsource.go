package jsonsource

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

const Key = "json_source"

// jsonLine mirrors the JSONL layout written by agentsupervisor's WriteResult, so a
// generated transaction file can be replayed by the supervisor without any
// intermediate conversion.
type jsonLine struct {
	Hash      string `json:"hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Value     string `json:"value"`
	Nonce     uint64 `json:"nonce"`
	Data      string `json:"data"`
}

// JSONSource implements TxSource by reading a JSONL transaction file
// (agent_transactions.jsonl, one JSON object per line).
type JSONSource struct {
	file    *os.File
	scanner *bufio.Scanner
	done    bool
	line    int
}

func NewJSONSource(filename string) (*JSONSource, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open JSONL file: %w", err)
	}

	return &JSONSource{
		file:    f,
		scanner: bufio.NewScanner(f),
	}, nil
}

func (j *JSONSource) ReadTxs(size int64) ([]transaction.Transaction, error) {
	if j.done {
		return nil, nil
	}

	ret := make([]transaction.Transaction, 0, size)

	for int64(len(ret)) < size {
		if !j.scanner.Scan() {
			j.close()

			if err := j.scanner.Err(); err != nil {
				return nil, fmt.Errorf("failed to read JSONL file: %w", err)
			}

			break
		}

		j.line++

		tx, err := j.line2Tx(j.scanner.Bytes())
		if err != nil {
			j.close()

			return nil, fmt.Errorf("decode JSONL line %d: %w", j.line, err)
		}

		ret = append(ret, *tx)
	}

	return ret, nil
}

func (j *JSONSource) close() {
	j.done = true
	_ = j.file.Close()
}

func (j *JSONSource) line2Tx(line []byte) (*transaction.Transaction, error) {
	var jl jsonLine
	if err := json.Unmarshal(line, &jl); err != nil {
		return nil, fmt.Errorf("unmarshal JSONL line: %w", err)
	}

	sender, err := utils.Hex2Addr(jl.Sender)
	if err != nil {
		return nil, fmt.Errorf("parse sender address: %w", err)
	}

	recipient, err := utils.Hex2Addr(jl.Recipient)
	if err != nil {
		return nil, fmt.Errorf("parse recipient address: %w", err)
	}

	value := new(big.Int)
	if _, ok := value.SetString(jl.Value, 10); !ok {
		return nil, fmt.Errorf("parse value %q", jl.Value)
	}

	tx := transaction.NewTransaction(sender, recipient, value, big.NewInt(0), jl.Nonce, time.Now())

	data, err := utils.Hex2Bytes(jl.Data)
	if err != nil {
		return nil, fmt.Errorf("parse tx data: %w", err)
	}

	tx.Data = data

	return tx, nil
}
