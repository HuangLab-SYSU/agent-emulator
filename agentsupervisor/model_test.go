package agentsupervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestChatModelConfigurableRequest(t *testing.T) {
	t.Setenv("AGENT_TEST_KEY", "test-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer test-secret", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "some-provider-model", body["model"])
		require.Contains(t, body["messages"].([]any)[0].(map[string]any)["content"], "summary field")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"action":"pay","target":"bob","amount":"12","summary":"当前余额充足，决定向 bob 转账 12。"}`}}}, "usage": map[string]int{"total_tokens": 42}}))
	}))
	defer server.Close()
	model := ChatModel{Config: ModelConfig{AgentID: "alice", Model: "some-provider-model", BaseURL: server.URL + "/v1/", APIKeyEnv: "AGENT_TEST_KEY"}, Client: server.Client()}
	reply, err := model.Decide(context.Background(), Observation{AgentID: "alice"})
	require.NoError(t, err)
	require.Equal(t, Decision{Action: "pay", Target: "bob", Amount: "12", Summary: "当前余额充足，决定向 bob 转账 12。"}, reply.Decision)
	require.Contains(t, string(reply.Raw), "total_tokens")
}

func TestChatModelInvalidAndCancellation(t *testing.T) {
	for _, content := range []string{`{"action":"steal"}`, `{"action":"wait","nonce":8}`, `{"action":"wait"} {"action":"pay"}`, "```json\n{}\n```"} {
		t.Run(content, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}}))
			}))
			defer server.Close()
			m := ChatModel{Config: ModelConfig{BaseURL: server.URL}, Client: server.Client()}
			_, err := m.Decide(context.Background(), Observation{})
			require.Error(t, err)
		})
	}
	t.Run("cancel", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		m := ChatModel{Config: ModelConfig{BaseURL: server.URL}, Client: server.Client()}
		_, err := m.Decide(ctx, Observation{})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("http-error-does-not-expose-body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "sensitive-body", http.StatusUnauthorized) }))
		defer server.Close()
		m := ChatModel{Config: ModelConfig{BaseURL: server.URL}, Client: server.Client()}
		_, err := m.Decide(context.Background(), Observation{})
		require.ErrorContains(t, err, "401")
		require.NotContains(t, err.Error(), "sensitive-body")
	})
}

func TestModelConfigDefaultsAndExplicitZero(t *testing.T) {
	for _, budget := range []string{"", "  max_blocks_during_decision: 0\n"} {
		cfgFile := t.TempDir() + "/config.yaml"
		text := "base:\n  result_dir: ./out\nexperiment:\n  trace: ./trace.jsonl\nchain:\n  enabled: true\nbase_unused: ignored\nprotocols:\n  pay:\n    plugin: direct-pay\n  identity:\n    plugin: did-simple\nagent_api:\n  enabled: true\n  agent_id: alice\n  base_url: http://localhost:1234/v1\n  model: model-a\n" + budget
		text = strings.Replace(text, "  result_dir: ./out", "  result_dir: ./out\n  blockemulator_config: config.yaml", 1)
		require.NoError(t, os.WriteFile(cfgFile, []byte(text), 0o600))
		cfg, err := LoadConfig(cfgFile)
		require.NoError(t, err)
		expected := uint64(5)
		if budget != "" {
			expected = 0
		}
		require.Equal(t, expected, *cfg.AgentAPI.MaxBlocksDuringDecision)
		require.Equal(t, "chat-completions", cfg.AgentAPI.Adapter)
	}
}

func TestModelAPIKeyFile(t *testing.T) {
	t.Run("trim newline and reread on rotation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "key")
		require.NoError(t, os.WriteFile(path, []byte("  local-test-key\r\n"), 0o600))
		cfg := ModelConfig{APIKeyFile: path}
		key, err := cfg.loadAPIKey()
		require.NoError(t, err)
		require.Equal(t, "local-test-key", key)
		require.NoError(t, os.WriteFile(path, []byte("rotated-test-key\n"), 0o600))
		key, err = cfg.loadAPIKey()
		require.NoError(t, err)
		require.Equal(t, "rotated-test-key", key)
	})
	for _, item := range []struct{ name, content, want string }{
		{"empty", " \n", "empty"},
		{"multiline", "first-secret\nsecond-secret", "single token"},
		{"oversized", strings.Repeat("x", 16385), "16 KiB"},
	} {
		t.Run(item.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			require.NoError(t, os.WriteFile(path, []byte(item.content), 0o600))
			_, err := (ModelConfig{APIKeyFile: path}).loadAPIKey()
			require.ErrorContains(t, err, item.want)
			require.NotContains(t, err.Error(), "first-secret")
		})
	}
	t.Run("missing", func(t *testing.T) {
		_, err := (ModelConfig{APIKeyFile: filepath.Join(t.TempDir(), "missing")}).loadAPIKey()
		require.ErrorContains(t, err, "open API key file")
	})
	t.Run("conflicting sources", func(t *testing.T) {
		_, err := (ModelConfig{APIKeyFile: "key", APIKeyEnv: "ENV_KEY"}).loadAPIKey()
		require.ErrorContains(t, err, "only one")
	})
}

func TestModelKeyPathRelativeToYAMLAndSavedConfig(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "local.key")
	require.NoError(t, os.WriteFile(keyPath, []byte("local-secret-not-for-snapshot"), 0o600))
	cfg := testConfig(t)
	cfg.Chain.Enabled = true
	cfg.Base.BlockEmulatorConfig = "unused.yaml"
	cfg.Experiment.Trace = "unused.jsonl"
	cfg.AgentAPI = ModelConfig{Enabled: true, AgentID: "alice", BaseURL: "http://localhost/v1", Model: "local", APIKeyFile: "local.key"}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	configPath := filepath.Join(dir, "agentEmuConfig.yaml")
	require.NoError(t, os.WriteFile(configPath, data, 0o600))
	loaded, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.Equal(t, keyPath, loaded.AgentAPI.APIKeyFile)
	data, err = yaml.Marshal(loaded)
	require.NoError(t, err)
	require.NotContains(t, string(data), "local-secret-not-for-snapshot")
	savedPath := filepath.Join(t.TempDir(), "hybrid_config.yaml")
	require.NoError(t, os.WriteFile(savedPath, data, 0o600))
	reloaded, err := LoadConfig(savedPath)
	require.NoError(t, err)
	require.Equal(t, keyPath, reloaded.AgentAPI.APIKeyFile)
	key, err := reloaded.AgentAPI.loadAPIKey()
	require.NoError(t, err)
	require.Equal(t, "local-secret-not-for-snapshot", key)
}

func TestChatModelUsesKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("local-file-key\n"), 0o600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer local-file-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"action":"wait"}`}}}}))
	}))
	defer server.Close()
	model := ChatModel{Config: ModelConfig{BaseURL: server.URL, APIKeyFile: path}, Client: server.Client()}
	reply, err := model.Decide(context.Background(), Observation{})
	require.NoError(t, err)
	require.Equal(t, "wait", reply.Decision.Action)
}
