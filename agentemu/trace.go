package agentemu

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type Action string

const (
	ActionPay   Action = "pay"
	ActionJoin  Action = "join"
	ActionLeave Action = "leave"
	// ActionRawTx marks a plain transfer line. It never appears in trace
	// files: LoadTrace detects such lines structurally (a "sender" key) and
	// synthesizes this action.
	ActionRawTx Action = "raw_tx"
)

// RawTxSpec is a plain (non-agent) transfer line. Only sender, recipient and
// value are inputs; the Host determines nonce and data exactly like for agent
// transactions, so extra fields on a copied plan line are ignored.
type RawTxSpec struct {
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Value     string `json:"value"`
}

// Record is the smallest deterministic input unit for AgentEmulator. A record
// is either an agent action or, when RawTx is set, a plain transfer line.
// TS uses Unix milliseconds and Seq preserves original ordering for equal timestamps.
// Plain transfer lines carry no ts; they inherit the previous line's TS so the
// (TS, Seq) sort keeps them at their file position.
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

func LoadTrace(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open trace: %w", err)
	}

	defer func() { _ = f.Close() }()

	var records []Record

	scanner := bufio.NewScanner(f)
	lastTS := int64(0)

	for line := 1; scanner.Scan(); line++ {
		// Both line kinds decode with one Unmarshal: a plain transfer line is
		// detected by its "sender" key, everything else is an agent action.
		var probe struct {
			Record
			RawTxSpec
		}
		if err := json.Unmarshal(scanner.Bytes(), &probe); err != nil {
			return nil, fmt.Errorf("decode trace line %d: %w", line, err)
		}

		var record Record

		if probe.Sender != "" {
			if probe.AgentID != "" || probe.Action != "" {
				return nil, fmt.Errorf("ambiguous trace line %d: a plain transfer must not carry agent fields", line)
			}

			if probe.Recipient == "" || probe.Value == "" {
				return nil, fmt.Errorf("invalid plain transfer line %d: sender, recipient and value are required", line)
			}

			spec := probe.RawTxSpec
			record = Record{Action: ActionRawTx, TS: lastTS, RawTx: &spec}
		} else {
			record = probe.Record
			if record.AgentID == "" || record.Action == "" || record.TS < 0 {
				return nil, fmt.Errorf("invalid trace line %d", line)
			}

			lastTS = record.TS
		}

		record.Seq = line
		records = append(records, record)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read trace: %w", err)
	}

	sort.SliceStable(records, func(i, j int) bool {
		if records[i].TS == records[j].TS {
			return records[i].Seq < records[j].Seq
		}

		return records[i].TS < records[j].TS
	})

	return records, nil
}
