package temporal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.temporal.io/sdk/testsuite"

	"github.com/angoo/agentfoundry-worker/internal/config"
	"github.com/angoo/agentfoundry-worker/internal/orchestrator"
)

func TestPreInferenceActivityMCPTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/mcp/call" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var request struct {
			Server    string         `json:"server"`
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Server != "accounts" || request.Tool != "current_user" || request.Arguments["id"] != "42" {
			t.Fatalf("unexpected request: %+v", request)
		}
		_, _ = w.Write([]byte(`{"content":"Current user: Ada","is_error":false}`))
	}))
	defer server.Close()

	activities := NewActivities(orchestrator.NewClient(orchestrator.Config{URL: server.URL}), nil)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activities.PreInferenceActivity)
	encodedResult, err := env.ExecuteActivity(activities.PreInferenceActivity, PreInferenceInput{
		Processor: config.PreInferenceProcessor{
			ID:        "account-context",
			Processor: "mcp_tool",
			Config:    json.RawMessage(`{"server":"accounts","tool":"current_user","arguments":{"id":"42"}}`),
		},
	})
	if err != nil {
		t.Fatalf("execute activity: %v", err)
	}
	var result PreInferenceResult
	if err := encodedResult.Get(&result); err != nil {
		t.Fatalf("decode activity result: %v", err)
	}
	if result.Text != "Current user: Ada" {
		t.Fatalf("got %q, want tool text", result.Text)
	}
}

func TestTruncatePreInferenceText(t *testing.T) {
	text := string(make([]byte, maxPreInferenceTextBytes+1))
	if got := truncatePreInferenceText(text); len(got) != maxPreInferenceTextBytes {
		t.Fatalf("got %d bytes, want %d", len(got), maxPreInferenceTextBytes)
	}
}
