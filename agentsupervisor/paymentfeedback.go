package agentsupervisor

import (
	"fmt"
	"math/big"
)

// settledPaymentsByTarget reports only this API identity's accepted outgoing
// payments whose final chain confirmation has cleared the pending set. Cumulative
// totals survive arbitrarily many wait decisions within this experiment.
func (h *HybridRuntime) settledPaymentsByTarget() (map[string]string, error) {
	totals := make(map[string]*big.Int)

	for _, audit := range h.audits {
		decision := audit.Reply.Decision
		if audit.Result != "accepted" || Action(decision.Action) != ActionPay || audit.TransactionHash == "" {
			continue
		}

		if _, pending := h.pending[audit.TransactionHash]; pending {
			continue
		}

		amount, ok := new(big.Int).SetString(decision.Amount, 10)
		if !ok || amount.Sign() <= 0 {
			return nil, fmt.Errorf("invalid accepted payment amount in decision audit")
		}

		if totals[decision.Target] == nil {
			totals[decision.Target] = new(big.Int)
		}

		totals[decision.Target].Add(totals[decision.Target], amount)
	}

	result := make(map[string]string, len(totals))
	for target, amount := range totals {
		result[target] = amount.String()
	}

	return result, nil
}
