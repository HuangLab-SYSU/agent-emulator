package agentsupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type thinkingTestModel struct {
	reply   ModelReply
	err     error
	release <-chan struct{}
}

func (m thinkingTestModel) Decide(ctx context.Context, _ Observation) (ModelReply, error) {
	select {
	case <-ctx.Done():
		return ModelReply{}, ctx.Err()
	case <-m.release:
		return m.reply, m.err
	}
}

func TestThinkingLogVisibleDuringDecision(t *testing.T) {
	cases := []struct {
		name     string
		decision Decision
		failure  error
		expected string
	}{
		{"pay", Decision{Action: "pay", Target: "bob", Amount: "5", Summary: "Available funds are sufficient to pay bob 5."}, nil, "accepted"},
		{"wait", Decision{Action: "wait"}, nil, "accepted"},
		{"leave", Decision{Action: "leave"}, nil, "accepted"},
		{"rejected", Decision{Action: "pay", Target: "bob", Amount: "50"}, nil, "rejected"},
		{"timeout", Decision{}, context.DeadlineExceeded, "deadline exceeded"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			h := testHybrid(t)
			now := time.Now()
			h.started = now
			require.NoError(t, h.injectTrace(context.Background(), now))
			release := make(chan struct{})
			h.model = thinkingTestModel{release: release, reply: ModelReply{Decision: item.decision, Attempts: 1, Raw: json.RawMessage(`{"reasoning_content":"raw-response-must-not-be-logged"}`)}, err: item.failure}
			obs := Observation{AgentID: "alice", ObservedAt: now, Heights: map[int64]uint64{0: 3}, Agents: []ObservedAgent{{AgentID: "alice", Active: true, AvailableBalance: "10"}, {AgentID: "bob", Active: true, AvailableBalance: "10"}}}
			path := filepath.Join(h.cfg.Base.ResultDir, AgentThinkingLogFile)
			readLog := func() string { data, err := os.ReadFile(path); require.NoError(t, err); return string(data) }
			h.startDecision(obs, now)
			text := readLog()
			require.Contains(t, text, "event=thinking_started", "start must be visible before the model returns or the file is closed")
			require.Contains(t, text, `msg="Agent started thinking"`)
			require.Contains(t, text, "decision=1")
			require.Contains(t, text, "agent_id=alice")
			require.NotContains(t, text, "event=thinking_finished")
			close(release)
			require.Eventually(t, func() bool { return len(h.resultCh) == 1 }, time.Second, time.Millisecond)
			require.NoError(t, h.pollModel(now.Add(2*time.Second)))
			text = readLog()
			require.Contains(t, text, "event=thinking_finished")
			require.Contains(t, text, `msg="Agent finished thinking"`)
			require.Contains(t, text, "duration_ms=2000")
			if item.failure == nil {
				require.NotContains(t, text, "event=decision_result", "receiving a model reply must not claim it passed validation")
				hash, err := h.applyDecision(context.Background(), item.decision, obs, now.Add(3*time.Second))
				result := "accepted"
				if err != nil {
					result = fmt.Sprintf("rejected: %s", err)
				}
				h.current.TransactionHash = hash
				h.finishAudit(result, now.Add(3*time.Second))
				text = readLog()
				require.Contains(t, text, "action="+item.decision.Action)
				if item.name == "pay" {
					require.Contains(t, text, "target=bob")
					require.Contains(t, text, "amount=5")
					require.Contains(t, text, `summary="Available funds are sufficient to pay bob 5."`)
					require.Contains(t, text, "transaction_hash="+hash)
					require.NotEmpty(t, hash)
				}
			}
			require.Contains(t, text, `msg="Agent decision result"`)
			require.Contains(t, text, item.expected)
			require.Equal(t, 1, strings.Count(text, "event=thinking_started"))
			require.Equal(t, 1, strings.Count(text, "event=thinking_finished"))
			require.Equal(t, 1, strings.Count(text, "event=decision_result"))
			require.Less(t, strings.Index(text, "event=thinking_started"), strings.Index(text, "event=thinking_finished"))
			require.Less(t, strings.Index(text, "event=thinking_finished"), strings.Index(text, "event=decision_result"))
			require.NotContains(t, text, "raw-response-must-not-be-logged")
		})
	}
}

func TestThinkingLogExperimentEndPairsStartAndFinish(t *testing.T) {
	h := testHybrid(t)
	now := time.Now()
	h.started = now.Add(-time.Duration(h.cfg.AgentAPI.RunDurationSeconds) * time.Second)
	h.model = thinkingTestModel{release: make(chan struct{})}
	h.startDecision(Observation{ObservedAt: now}, now)
	require.NoError(t, h.updateLifecycle(now))
	require.Eventually(t, func() bool { return len(h.resultCh) == 1 }, time.Second, time.Millisecond)
	data, err := os.ReadFile(filepath.Join(h.cfg.Base.ResultDir, AgentThinkingLogFile))
	require.NoError(t, err)
	text := string(data)
	require.Equal(t, 1, strings.Count(text, "event=thinking_finished"))
	require.Contains(t, text, "experiment duration reached")
	require.Contains(t, text, "status=error")
}

func TestThinkingSummary(t *testing.T) {
	for _, item := range []struct {
		name     string
		decision Decision
		failure  error
		want     string
	}{
		{"pay fallback", Decision{Action: "pay", Target: "bob", Amount: "5"}, nil, "Decided to pay bob 5."},
		{"wait fallback", Decision{Action: "wait"}, nil, "Decided to wait without submitting a transaction."},
		{"leave fallback", Decision{Action: "leave"}, nil, "Decided to leave."},
		{"single line", Decision{Action: "wait", Summary: "Waiting for funds.\n\t No payment yet."}, nil, "Waiting for funds. No payment yet."},
		{"Chinese summary falls back to English", Decision{Action: "pay", Target: "bob", Amount: "5", Summary: "Decided to pay. 余额充足。"}, nil, "Decided to pay bob 5."},
		{"bounded unicode", Decision{Summary: strings.Repeat("é", 121)}, nil, strings.Repeat("é", 120) + "…"},
		{"failed reply", Decision{Summary: "Decided to pay."}, context.DeadlineExceeded, "Decision failed. See the error field."},
	} {
		t.Run(item.name, func(t *testing.T) {
			require.Equal(t, item.want, thinkingSummary(item.decision, item.failure))
		})
	}
}
