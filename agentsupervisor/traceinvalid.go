package agentsupervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const InvalidTraceActionsFile = "invalid_trace_actions.jsonl"

var errInvalidTraceAction = errors.New("invalid trace action")

// InvalidTraceAction is an off-chain rejection: no transaction was generated or sent.
// TraceLine and Record.TS retain the original input line and scheduled time.
type InvalidTraceAction struct {
	TraceLine int        `json:"trace_line"`
	InvalidAt time.Time  `json:"invalid_at"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason"`
	Record    Record     `json:"record"`
	RawTx     *RawTxSpec `json:"raw_tx,omitempty"`
}

// Validate mutable lifecycle rules before Process can change the registry,
// increment a nonce or append an action-to-transaction link. Compiler and I/O
// errors remain fatal and must never be mistaken for a rejected trace action.
func (h *HybridRuntime) validateTraceAction(record Record) error {
	switch record.Action {
	case ActionJoin:
		if record.AgentID == "" {
			return fmt.Errorf("%w: agent_id is required", errInvalidTraceAction)
		}
	case ActionLeave:
		if _, err := h.compiler.registry.Active(record.AgentID); err != nil {
			return fmt.Errorf("%w: %w", errInvalidTraceAction, err)
		}
	case ActionPay:
		if _, err := h.compiler.registry.Active(record.AgentID); err != nil {
			return fmt.Errorf("%w: %w", errInvalidTraceAction, err)
		}

		if _, err := h.compiler.registry.Active(record.Target); err != nil {
			return fmt.Errorf("%w: %w", errInvalidTraceAction, err)
		}

		if record.Amount == 0 {
			return fmt.Errorf("%w: pay amount must be positive", errInvalidTraceAction)
		}
	case ActionRawTx:
		// Raw transfers have no agent lifecycle constraint; retain their compiler
		// validation and the existing input-file checks.
	default:
		return fmt.Errorf("%w: unsupported action %q", errInvalidTraceAction, record.Action)
	}

	return nil
}

func (h *HybridRuntime) recordInvalidTraceAction(record Record, cause error, now time.Time) error {
	invalid := InvalidTraceAction{
		TraceLine: record.Seq,
		InvalidAt: now,
		Status:    "invalid",
		Reason:    cause.Error(),
		Record:    record,
		RawTx:     record.RawTx,
	}
	if err := json.NewEncoder(h.invalidActions).Encode(invalid); err != nil {
		return fmt.Errorf("write invalid trace action: %w", err)
	}

	h.traceInvalid++

	slog.Warn("invalid trace action; continuing experiment",
		"trace_line", record.Seq, "agent_id", record.AgentID, "action", record.Action,
		"target", record.Target, "amount", record.Amount, "request_id", record.RequestID, "reason", cause.Error())

	return nil
}
