package agentemu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Agent is the durable mapping between an external agent ID and its DID.
// It deliberately lives outside account.State to preserve legacy account semantics.
type Agent struct {
	AgentID string `json:"agent_id"`
	DID     string `json:"did"`
	Active  bool   `json:"active"`
}

type Registry struct {
	seed   int64
	agents map[string]Agent
}

func LoadRegistry(path string, seed int64) (*Registry, error) {
	r := &Registry{seed: seed, agents: make(map[string]Agent)}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent registry: %w", err)
	}
	var agents []Agent
	if err := json.Unmarshal(b, &agents); err != nil {
		return nil, fmt.Errorf("decode agent registry: %w", err)
	}
	for _, agent := range agents {
		if agent.AgentID == "" || agent.DID == "" {
			return nil, fmt.Errorf("agent registry contains an invalid entry")
		}
		r.agents[agent.AgentID] = agent
	}
	return r, nil
}

func (r *Registry) Join(agentID string) (Agent, bool, error) {
	if agentID == "" {
		return Agent{}, false, fmt.Errorf("agent_id is required")
	}
	agent, exists := r.agents[agentID]
	if !exists {
		agent = Agent{AgentID: agentID, DID: allocatedDID(r.seed, agentID)}
	}
	wasActive := agent.Active
	agent.Active = true
	r.agents[agentID] = agent
	return agent, !wasActive, nil
}

func (r *Registry) Leave(agentID string) (Agent, error) {
	agent, exists := r.agents[agentID]
	if !exists || !agent.Active {
		return Agent{}, fmt.Errorf("agent %q is not active", agentID)
	}
	agent.Active = false
	r.agents[agentID] = agent
	return agent, nil
}

func (r *Registry) Active(agentID string) (Agent, error) {
	agent, exists := r.agents[agentID]
	if !exists || !agent.Active {
		return Agent{}, fmt.Errorf("agent %q is not active; a join action is required", agentID)
	}
	return agent, nil
}

func (r *Registry) Write(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create agent registry directory: %w", err)
	}
	keys := make([]string, 0, len(r.agents))
	for id := range r.agents {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	agents := make([]Agent, 0, len(keys))
	for _, id := range keys {
		agents = append(agents, r.agents[id])
	}
	b, err := json.MarshalIndent(agents, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent registry: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write agent registry: %w", err)
	}
	return nil
}

func allocatedDID(seed int64, agentID string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", seed, agentID)))
	return "did:broker:0x" + hex.EncodeToString(hash[len(hash)-20:])
}
