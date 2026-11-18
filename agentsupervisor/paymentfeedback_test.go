package agentsupervisor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservationTracksOnlySettledAPIPayments(t *testing.T) {
	h := testHybrid(t)
	now := time.Now()
	h.started = now
	makeAudit := func(target, amount, hash, result string) DecisionAudit {
		return DecisionAudit{Reply: ModelReply{Decision: Decision{Action: "pay", Target: target, Amount: amount}}, TransactionHash: hash, Result: result}
	}
	h.audits = []DecisionAudit{
		makeAudit("audit-backup", "80", "audit", "accepted"),
		makeAudit("finance", "50", "refund-1", "accepted"),
		makeAudit("finance", "150", "refund-2", "accepted"),
		makeAudit("storage-backup", "120", "storage", "accepted"),
		makeAudit("other", "999", "rejected", "rejected"),
		makeAudit("other", "999", "", "accepted"),
		{Reply: ModelReply{Decision: Decision{Action: "wait"}}, Result: "accepted"},
	}
	h.pending["storage"] = &pendingPayment{}
	obs, err := h.buildObservation(now.Add(125 * time.Second))
	require.NoError(t, err)
	require.EqualValues(t, 125000, obs.ElapsedMS)
	require.Equal(t, map[string]string{"audit-backup": "80", "finance": "200"}, obs.SettledPaymentsByTarget)
	delete(h.pending, "storage")
	// Many waits must not erase task-completion evidence.
	for i := 0; i < 50; i++ {
		h.audits = append(h.audits, DecisionAudit{Reply: ModelReply{Decision: Decision{Action: "wait"}}, Result: "accepted"})
	}
	obs, err = h.buildObservation(now.Add(200 * time.Second))
	require.NoError(t, err)
	require.Equal(t, "120", obs.SettledPaymentsByTarget["storage-backup"])
	require.Equal(t, "80", obs.SettledPaymentsByTarget["audit-backup"])
	require.Equal(t, "200", obs.SettledPaymentsByTarget["finance"])
	require.NotContains(t, obs.SettledPaymentsByTarget, "other")
}
