package agentemu

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/utils"
)

const contractABI = `[
 {"type":"function","name":"append","inputs":[{"name":"entryHash","type":"bytes32"}]},
 {"type":"function","name":"anchor","inputs":[{"name":"root","type":"bytes32"},{"name":"count","type":"uint256"}]},
 {"type":"function","name":"register","inputs":[{"name":"didHash","type":"bytes32"},{"name":"docHash","type":"bytes32"}]},
 {"type":"function","name":"revoke","inputs":[{"name":"didHash","type":"bytes32"}]}
]`

type MetricEvent struct {
	Kind      string
	TS        int64
	RequestID string
	Value     uint64
}

type Result struct {
	Transactions []transaction.Transaction
	Metrics      []MetricEvent
}

type auditEntry struct {
	hash [32]byte
	from account.Address
	ts   int64
}

// Host compiles lifecycle, payment and audit actions into existing transactions.
type Host struct {
	cfg      Config
	abi      abi.ABI
	nonces   map[account.Address]uint64
	registry *Registry
	pending  []auditEntry
	metrics  []MetricEvent
	txs      []transaction.Transaction
}

func NewHost(cfg Config) (*Host, error) {
	parsed, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		return nil, fmt.Errorf("parse built-in contract ABI: %w", err)
	}
	registry, err := LoadRegistry(filepath.Join(cfg.Base.ResultDir, "agent_registry.json"), cfg.Experiment.Seed)
	if err != nil {
		return nil, err
	}
	return &Host{cfg: cfg, abi: parsed, nonces: make(map[account.Address]uint64), registry: registry}, nil
}

func (h *Host) Process(records []Record) (Result, error) {
	for _, record := range records {
		if err := h.process(record); err != nil {
			return Result{}, fmt.Errorf("process trace line %d: %w", record.Seq, err)
		}
	}
	if h.cfg.Protocols.Audit.Plugin == "merkle-audit" {
		if err := h.flushAudit(); err != nil {
			return Result{}, err
		}
	}
	return Result{Transactions: h.txs, Metrics: h.metrics}, nil
}

func (h *Host) process(record Record) error {
	switch record.Action {
	case ActionJoin:
		return h.processJoin(record)
	case ActionLeave:
		return h.processLeave(record)
	case ActionPay:
		if err := h.processPay(record); err != nil {
			return err
		}
		return h.processAudit(record)
	case ActionAppendLog:
		return h.processAudit(record)
	default:
		return fmt.Errorf("unsupported action %q", record.Action)
	}
}

func (h *Host) processJoin(record Record) error {
	agent, changed, err := h.registry.Join(record.AgentID)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := h.processIdentity(record, agent, "register"); err != nil {
		return err
	}
	return h.processAuditForAgent(record, agent)
}

func (h *Host) processLeave(record Record) error {
	agent, err := h.registry.Leave(record.AgentID)
	if err != nil {
		return err
	}
	if err := h.processIdentity(record, agent, "revoke"); err != nil {
		return err
	}
	return h.processAuditForAgent(record, agent)
}

func (h *Host) processPay(record Record) error {
	fromAgent, err := h.registry.Active(record.AgentID)
	if err != nil {
		return err
	}
	toAgent, err := h.registry.Active(record.Target)
	if err != nil {
		return err
	}
	from, err := didAddress(fromAgent.DID)
	if err != nil {
		return err
	}
	to, err := didAddress(toAgent.DID)
	if err != nil {
		return err
	}
	h.appendTx(from, to, record.Amount, nil, record.TS)
	h.metric("pay_onchain", record)
	return nil
}

func (h *Host) processAudit(record Record) error {
	agent, err := h.registry.Active(record.AgentID)
	if err != nil {
		return err
	}
	return h.processAuditForAgent(record, agent)
}

func (h *Host) processAuditForAgent(record Record, agent Agent) error {
	from, err := didAddress(agent.DID)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(string(record.Action) + ":" + record.AgentID + ":" + record.Target + ":" + record.ParamsHash + ":" + record.RequestID)) //nolint:golines
	switch h.cfg.Protocols.Audit.Plugin {
	case "onchain-audit":
		data, err := h.abi.Pack("append", hash)
		if err != nil {
			return fmt.Errorf("pack audit append: %w", err)
		}
		if err := h.appendContractTx(from, h.cfg.Protocols.Audit.ContractAddress, data, record.TS); err != nil {
			return err
		}
		h.metric("audit_onchain", record)
	case "merkle-audit":
		h.pending = append(h.pending, auditEntry{hash: hash, from: from, ts: record.TS})
		h.metric("audit_buffered", record)
		if len(h.pending) >= h.cfg.Protocols.Audit.BatchSize {
			return h.flushAudit()
		}
	default:
		return fmt.Errorf("unknown audit plugin %q", h.cfg.Protocols.Audit.Plugin)
	}
	return nil
}

func (h *Host) flushAudit() error {
	if len(h.pending) == 0 {
		return nil
	}
	leaves := make([][32]byte, len(h.pending))
	for i, entry := range h.pending {
		leaves[i] = entry.hash
	}
	root := merkleRoot(leaves)
	data, err := h.abi.Pack("anchor", root, new(big.Int).SetUint64(uint64(len(leaves))))
	if err != nil {
		return fmt.Errorf("pack audit anchor: %w", err)
	}
	last := h.pending[len(h.pending)-1]
	if err := h.appendContractTx(last.from, h.cfg.Protocols.Audit.ContractAddress, data, last.ts); err != nil {
		return err
	}
	h.metrics = append(h.metrics, MetricEvent{Kind: "audit_anchor", TS: last.ts, Value: uint64(len(h.pending))})
	h.pending = nil
	return nil
}

func (h *Host) processIdentity(record Record, agent Agent, operation string) error {
	from, err := didAddress(agent.DID)
	if err != nil {
		return err
	}
	didHash := sha256.Sum256([]byte(agent.DID))
	var data []byte
	switch operation {
	case "register":
		docHash := sha256.Sum256([]byte(record.ParamsHash))
		data, err = h.abi.Pack("register", didHash, docHash)
	case "revoke":
		data, err = h.abi.Pack("revoke", didHash)
	default:
		return fmt.Errorf("unknown DID operation %q", operation)
	}
	if err != nil {
		return fmt.Errorf("pack DID operation: %w", err)
	}
	if err := h.appendContractTx(from, h.cfg.Protocols.Identity.ContractAddress, data, record.TS); err != nil {
		return err
	}
	h.metric("did_"+operation, record)
	return nil
}

func (h *Host) appendTx(from, to account.Address, amount uint64, data []byte, ts int64) {
	nonce := h.nonces[from]
	h.nonces[from]++
	tx := transaction.NewTransaction(from, to, new(big.Int).SetUint64(amount), big.NewInt(0), nonce, time.UnixMilli(ts))
	tx.Data = data
	h.txs = append(h.txs, *tx)
}

func (h *Host) appendContractTx(from account.Address, address string, data []byte, ts int64) error {
	to, err := utils.Hex2Addr(address)
	if err != nil {
		return fmt.Errorf("parse contract address: %w", err)
	}
	h.appendTx(from, to, 0, data, ts)
	return nil
}

func (h *Host) metric(kind string, record Record) {
	h.metrics = append(h.metrics, MetricEvent{Kind: kind, TS: record.TS, RequestID: record.RequestID, Value: record.Amount})
}

func (h *Host) WriteResult(result Result) error {
	if err := os.MkdirAll(h.cfg.Base.ResultDir, 0o755); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	if err := h.registry.Write(filepath.Join(h.cfg.Base.ResultDir, "agent_registry.json")); err != nil {
		return err
	}
	return writeResult(h.cfg.Base.ResultDir, result)
}

func didAddress(did string) (account.Address, error) {
	const prefix = "did:broker:"
	if !strings.HasPrefix(did, prefix) {
		return account.Address{}, fmt.Errorf("invalid DID %q", did)
	}
	hexAddr := strings.TrimPrefix(did, prefix)
	if len(strings.TrimPrefix(hexAddr, "0x")) != 40 {
		return account.Address{}, fmt.Errorf("DID must carry a 20-byte address")
	}
	addr, err := utils.Hex2Addr(hexAddr)
	if err != nil {
		return account.Address{}, fmt.Errorf("parse DID address: %w", err)
	}
	return addr, nil
}

func merkleRoot(leaves [][32]byte) [32]byte {
	for len(leaves) > 1 {
		if len(leaves)%2 == 1 {
			leaves = append(leaves, leaves[len(leaves)-1])
		}
		next := make([][32]byte, 0, len(leaves)/2)
		for i := 0; i < len(leaves); i += 2 {
			next = append(next, sha256.Sum256(append(leaves[i][:], leaves[i+1][:]...)))
		}
		leaves = next
	}
	return leaves[0]
}

func writeResult(dir string, result Result) error {
	plan, err := os.Create(filepath.Join(dir, "agent_transactions.jsonl"))
	if err != nil {
		return fmt.Errorf("create transaction plan: %w", err)
	}
	defer plan.Close()
	for _, tx := range result.Transactions {
		hash, err := tx.Hash()
		if err != nil {
			return fmt.Errorf("hash planned transaction: %w", err)
		}
		if _, err := fmt.Fprintf(plan, "{\"hash\":\"%x\",\"sender\":\"0x%x\",\"recipient\":\"0x%x\",\"value\":\"%s\",\"nonce\":%d,\"data\":\"0x%x\"}\n", hash, tx.Sender, tx.Recipient, tx.Value, tx.Nonce, tx.Data); err != nil {
			return fmt.Errorf("write transaction plan: %w", err)
		}
	}
	metrics, err := os.Create(filepath.Join(dir, "Agent_Events.csv"))
	if err != nil {
		return fmt.Errorf("create metrics: %w", err)
	}
	defer metrics.Close()
	if _, err := fmt.Fprintln(metrics, "kind,ts,request_id,value"); err != nil {
		return fmt.Errorf("write metrics header: %w", err)
	}
	for _, event := range result.Metrics {
		if _, err := fmt.Fprintf(metrics, "%s,%d,%s,%d\n", event.Kind, event.TS, event.RequestID, event.Value); err != nil {
			return fmt.Errorf("write metric: %w", err)
		}
	}
	return nil
}
