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
	ActionPay       Action = "pay"
	ActionAppendLog Action = "append_log"
	ActionJoin      Action = "join"
	ActionLeave     Action = "leave"
)

// Record is the smallest deterministic input unit for AgentEmulator.
// TS uses Unix milliseconds and Seq preserves original ordering for equal timestamps.
type Record struct {
	AgentID    string `json:"agent_id"`
	Action     Action `json:"action"`
	Target     string `json:"target"`
	Amount     uint64 `json:"amount"`
	TS         int64  `json:"ts"`
	RequestID  string `json:"request_id"`
	ParamsHash string `json:"params_hash"`
	Seq        int    `json:"-"`
}

func LoadTrace(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open trace: %w", err)
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode trace line %d: %w", line, err)
		}
		if record.AgentID == "" || record.Action == "" || record.TS < 0 {
			return nil, fmt.Errorf("invalid trace line %d", line)
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
