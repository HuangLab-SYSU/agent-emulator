package agentsupervisor

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// AgentThinkingLogFile is a live, unbuffered log alongside hybrid_summary.json.
const AgentThinkingLogFile = "agent_thinking.log"

func newThinkingLogger(dir, agentID, model string) (*slog.Logger, *os.File, error) {
	file, err := os.OpenFile(filepath.Join(dir, AgentThinkingLogFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("create agent thinking log: %w", err)
	}
	// Independent INFO level keeps agent activity visible even if chain logging
	// is configured at WARN. Each line is written directly to the file and stdout.
	logger := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stdout, file), &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("agent_id", agentID, "model", model)

	return logger, file, nil
}

func (h *HybridRuntime) logThinkingStarted(obs Observation) {
	balance, available := "", ""

	for _, agent := range obs.Agents {
		if agent.AgentID == obs.AgentID {
			balance = agent.Balance
			available = agent.AvailableBalance

			break
		}
	}

	h.thinkingLogger.Info("Agent started thinking",
		"event", "thinking_started",
		"decision", h.decision,
		"observed_at", obs.ObservedAt,
		"elapsed_ms", obs.ElapsedMS,
		"settled_payments_by_target", obs.SettledPaymentsByTarget,
		"balance", balance,
		"available_balance", available,
		"shard_heights", obs.Heights,
		"max_blocks_during_decision", *h.cfg.AgentAPI.MaxBlocksDuringDecision)
}

func (h *HybridRuntime) logThinkingFinished(failure error) {
	status := "ok"
	errorText := ""

	if failure != nil {
		status = "error"
		errorText = failure.Error()
	}

	decision := h.current.Reply.Decision
	h.thinkingLogger.Info("Agent finished thinking",
		"event", "thinking_finished",
		"decision", h.decision,
		"started_at", h.current.Started,
		"finished_at", h.current.Finished,
		"duration_ms", h.current.DurationMS,
		"status", status,
		"summary", thinkingSummary(decision, failure),
		"action", decision.Action,
		"target", decision.Target,
		"amount", decision.Amount,
		"attempts", h.current.Reply.Attempts,
		"error", errorText)
}

func (h *HybridRuntime) logDecisionResult() {
	decision := h.current.Reply.Decision
	h.thinkingLogger.Info("Agent decision result",
		"event", "decision_result",
		"decision", h.decision,
		"action", decision.Action,
		"target", decision.Target,
		"amount", decision.Amount,
		"result", h.current.Result,
		"summary", thinkingSummary(decision, nil),
		"transaction_hash", h.current.TransactionHash,
		"blocks_granted", h.current.BlocksGranted,
		"blocks_committed", h.current.BlocksCommitted)
}

// Keep live summaries brief and use an English fallback if the model returns Chinese text.
func thinkingSummary(decision Decision, failure error) string {
	if failure != nil {
		return "Decision failed. See the error field."
	}

	summary := strings.Join(strings.Fields(decision.Summary), " ")
	if summary == "" || strings.ContainsFunc(summary, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		switch decision.Action {
		case string(ActionPay):
			summary = fmt.Sprintf("Decided to pay %s %s.", decision.Target, decision.Amount)
		case decisionActionWait:
			summary = "Decided to wait without submitting a transaction."
		case string(ActionLeave):
			summary = "Decided to leave."
		default:
			summary = "No valid decision was received."
		}
	}

	runes := []rune(summary)
	if len(runes) > 120 {
		return string(runes[:120]) + "…"
	}

	return summary
}
