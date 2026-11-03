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
		{"pay", Decision{Action: "pay", Target: "bob", Amount: "5", Summary: "当前余额充足，决定向 bob 转账 5。"}, nil, "accepted"},
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
			require.Contains(t, text, "decision=1")
			require.Contains(t, text, "agent_id=alice")
			require.NotContains(t, text, "event=thinking_finished")
			close(release)
			require.Eventually(t, func() bool { return len(h.resultCh) == 1 }, time.Second, time.Millisecond)
			require.NoError(t, h.pollModel(now.Add(2*time.Second)))
			text = readLog()
			require.Contains(t, text, "event=thinking_finished")
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
					require.Contains(t, text, `summary="当前余额充足，决定向 bob 转账 5。"`)
					require.Contains(t, text, "transaction_hash="+hash)
					require.NotEmpty(t, hash)
				}
			}
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
		{"pay fallback", Decision{Action: "pay", Target: "bob", Amount: "5"}, nil, "决定向 bob 转账 5。"},
		{"wait fallback", Decision{Action: "wait"}, nil, "决定继续等待，本轮不发起交易。"},
		{"leave fallback", Decision{Action: "leave"}, nil, "决定退出。"},
		{"single line", Decision{Action: "wait", Summary: "等待资金到账。\n\t 暂不付款。"}, nil, "等待资金到账。 暂不付款。"},
		{"bounded unicode", Decision{Summary: strings.Repeat("等", 121)}, nil, strings.Repeat("等", 120) + "…"},
		{"failed reply", Decision{Summary: "决定付款。"}, context.DeadlineExceeded, "本次未能完成决策，请查看 error 字段。"},
	} {
		t.Run(item.name, func(t *testing.T) {
			require.Equal(t, item.want, thinkingSummary(item.decision, item.failure))
		})
	}
}
