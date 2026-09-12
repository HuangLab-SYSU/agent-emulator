package plansource

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

const Key = "plan_source"

// planLine mirrors the JSONL layout written by agentemu's WriteResult, so a
// generated transaction plan can be replayed by the supervisor without any
// intermediate conversion.
type planLine struct {
	Hash      string `json:"hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Value     string `json:"value"`
	Nonce     uint64 `json:"nonce"`
	Data      string `json:"data"`
}

// PlanSource implements TxSource by reading a planned transaction file
// (agent_transactions.jsonl, one JSON object per line).
type PlanSource struct {
	file    *os.File
	scanner *bufio.Scanner
	done    bool
	line    int
}

func NewPlanSource(filename string) (*PlanSource, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open plan file: %w", err)
	}

	return &PlanSource{
		file:    f,
		scanner: bufio.NewScanner(f),
	}, nil
}

func (p *PlanSource) ReadTxs(size int64) ([]transaction.Transaction, error) {
	if p.done {
		return nil, nil
	}

	ret := make([]transaction.Transaction, 0, size)

	for int64(len(ret)) < size {
		if !p.scanner.Scan() {
			p.close()

			if err := p.scanner.Err(); err != nil {
				return nil, fmt.Errorf("failed to read plan file: %w", err)
			}

			break
		}

		p.line++

		tx, err := p.line2Tx(p.scanner.Bytes())
		if err != nil {
			return nil, fmt.Errorf("decode plan line %d: %w", p.line, err)
		}

		ret = append(ret, *tx)
	}

	return ret, nil
}

func (p *PlanSource) close() {
	p.done = true
	_ = p.file.Close()
}

func (p *PlanSource) line2Tx(line []byte) (*transaction.Transaction, error) {
	var pl planLine
	if err := json.Unmarshal(line, &pl); err != nil {
		return nil, fmt.Errorf("unmarshal plan line: %w", err)
	}

	sender, err := utils.Hex2Addr(pl.Sender)
	if err != nil {
		return nil, fmt.Errorf("parse sender address: %w", err)
	}

	recipient, err := utils.Hex2Addr(pl.Recipient)
	if err != nil {
		return nil, fmt.Errorf("parse recipient address: %w", err)
	}

	value := new(big.Int)
	if _, ok := value.SetString(pl.Value, 10); !ok {
		return nil, fmt.Errorf("parse value %q", pl.Value)
	}

	tx := transaction.NewTransaction(sender, recipient, value, big.NewInt(0), pl.Nonce, time.Now())

	data, err := utils.Hex2Bytes(pl.Data)
	if err != nil {
		return nil, fmt.Errorf("parse tx data: %w", err)
	}

	tx.Data = data

	return tx, nil
}
