package temporal

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/angoo/agentfoundry-worker/internal/config"
	"github.com/angoo/agentfoundry-worker/internal/llm"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestBackupAfterTwoPrimaryAttemptsAndStickyAcrossToolCalls(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	def := &config.Definition{AgentID: "a", Name: "assistant", Model: "primary", BackupProviderID: "backup-provider", BackupModel: "backup-model", SystemPrompt: "Help", MaxTurns: 5}
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: def}, nil)
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{ToolRoutes: []ToolRoute{{LLMName: "lookup", Kind: ToolKindMCP, ServerName: "server", ToolName: "lookup"}}}, nil)
	primary, backup, tools := 0, 0, 0
	env.OnActivity("CallToolActivity", mock.Anything, mock.Anything).Return(func(context.Context, CallToolInput) (CallToolResult, error) {
		tools++
		return CallToolResult{Content: "tool result"}, nil
	})
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if !in.ManagedRetries {
			t.Error("nested retries were not disabled")
		}
		if !in.Backup {
			primary++
			return LLMChatResult{}, fmt.Errorf("provider unavailable")
		}
		backup++
		if primary != 2 || in.Request.Model != "backup-model" || in.FailoverReason == "" {
			t.Errorf("unexpected switch: primary=%d input=%+v", primary, in)
		}
		message := llm.Message{Role: "assistant", Content: "done"}
		if backup == 1 {
			message.Content = nil
			message.ToolCalls = []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "lookup", Arguments: "{}"}}}
		} else {
			found := false
			for _, m := range in.Request.Messages {
				if m.Role == "tool" && m.Content == "tool result" {
					found = true
				}
			}
			if !found {
				t.Error("backup lost completed tool results")
			}
		}
		return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Message: message}}}}, nil
	})
	env.ExecuteWorkflow(RunAgentWorkflow, RunAgentParams{AgentID: "a", Message: "hello"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if primary != 2 || backup != 2 || tools != 1 {
		t.Fatalf("primary=%d backup=%d tool executions=%d", primary, backup, tools)
	}
	var result RunAgentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatal(err)
	}
	if !result.BackupActive || result.Response != "done" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestBackupFailurePolicies(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		failure                 error
		wantPrimary, wantBackup int
	}{
		{"invalid request", temporal.NewNonRetryableApplicationError("invalid request", "NonRetryable", nil), 1, 0},
		{"canceled", temporal.NewCanceledError(), 1, 0},
		{"both fail", fmt.Errorf("provider unavailable"), 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterActivity(&Activities{})
			env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: &config.Definition{AgentID: "a", Name: "a", BackupProviderID: "b"}}, nil)
			env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{}, nil)
			primary, backup := 0, 0
			env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
				if in.Backup {
					backup++
				} else {
					primary++
				}
				return LLMChatResult{}, tc.failure
			})
			env.ExecuteWorkflow(RunAgentWorkflow, RunAgentParams{AgentID: "a", Message: "hello"})
			err := env.GetWorkflowError()
			if err == nil || primary != tc.wantPrimary || backup != tc.wantBackup {
				t.Fatalf("err=%v primary=%d backup=%d", err, primary, backup)
			}
			if tc.name == "both fail" && !strings.Contains(err.Error(), "backup failed") {
				t.Fatal("missing backup failure context")
			}
		})
	}
}

func TestBackupStateResumesWithoutPrimary(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: &config.Definition{AgentID: "a", Name: "a", BackupProviderID: "b", BackupModel: "backup"}}, nil)
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{}, nil)
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if !in.Backup || in.Request.Model != "backup" {
			t.Error("resumed persistent run returned to primary")
		}
		return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}}}}, nil
	})
	env.ExecuteWorkflow(RunAgentWorkflow, RunAgentParams{AgentID: "a", BackupActive: true, BackupReason: "prior failure"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentBackupStaysActiveAcrossInputs(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: &config.Definition{AgentID: "a", Name: "a", BackupProviderID: "b"}}, nil)
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{}, nil)
	env.OnActivity("PublishTurnResultActivity", mock.Anything, mock.Anything).Return(nil)
	primary, backup := 0, 0
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if !in.Backup {
			primary++
			return LLMChatResult{}, fmt.Errorf("unavailable")
		}
		backup++
		return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}}}}, nil
	})
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(PersistentInputSignal, PersistentInput{InputID: "one", Message: "first"}) }, time.Millisecond)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(PersistentInputSignal, PersistentInput{InputID: "two", Message: "second"}) }, 3*time.Second)
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, 5*time.Second)
	env.ExecuteWorkflow(PersistentRunWorkflow, RunAgentParams{AgentID: "a"})
	if primary != 2 || backup != 2 {
		t.Fatalf("persistent routing primary=%d backup=%d", primary, backup)
	}
}

func TestHandoffChildUsesItsOwnBackup(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	env.RegisterWorkflow(RunAgentWorkflow)
	parent := &config.Definition{AgentID: "parent", Name: "parent", HandoffTo: "child"}
	child := &config.Definition{AgentID: "child", Name: "child", ProviderID: "child-primary", BackupProviderID: "child-backup"}
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in ResolveAgentInput) (ResolveAgentResult, error) {
		if in.AgentID == "parent" {
			return ResolveAgentResult{Definition: parent}, nil
		}
		return ResolveAgentResult{Definition: child}, nil
	})
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in BuildToolDefsInput) (BuildToolDefsResult, error) {
		if in.Definition.AgentID == "parent" {
			return BuildToolDefsResult{HandoffTo: &ToolRoute{AgentID: "child"}}, nil
		}
		return BuildToolDefsResult{}, nil
	})
	primary, backup := 0, 0
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if in.AgentID == "child" {
			if !in.Backup {
				primary++
				return LLMChatResult{}, fmt.Errorf("unavailable")
			}
			backup++
			if in.ProviderID != "child-backup" {
				t.Errorf("wrong child provider: %s", in.ProviderID)
			}
		}
		return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}}}}, nil
	})
	env.ExecuteWorkflow(RunAgentWorkflow, RunAgentParams{AgentID: "parent"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if primary != 2 || backup != 1 {
		t.Fatalf("child routing primary=%d backup=%d", primary, backup)
	}
}

func TestLegacyWorkflowVersionRetainsOriginalActivityPayload(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	env.OnGetVersion("backup-inference-v1", workflow.DefaultVersion, workflow.Version(1)).Return(workflow.DefaultVersion)
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: &config.Definition{AgentID: "a", Name: "a", BackupProviderID: "b"}}, nil)
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{}, nil)
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if in.ManagedRetries || in.Backup || in.ProviderID != "" {
			t.Errorf("legacy payload changed: %+v", in)
		}
		return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}}}}, nil
	})
	env.ExecuteWorkflow(RunAgentWorkflow, RunAgentParams{AgentID: "a"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}
