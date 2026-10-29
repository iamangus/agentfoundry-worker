package temporal

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/angoo/agentfoundry-worker/internal/config"
	"github.com/angoo/agentfoundry-worker/internal/llm"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func primaryRecoveryEnvironment() *testsuite.TestWorkflowEnvironment {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	def := &config.Definition{AgentID: "a", Name: "assistant", ProviderID: "primary-provider", Model: "primary", BackupProviderID: "backup-provider", BackupModel: "backup", SystemPrompt: "Help", MaxTurns: 5}
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: def}, nil)
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{ToolRoutes: []ToolRoute{{LLMName: "lookup", Kind: ToolKindMCP, ServerName: "server", ToolName: "lookup"}}}, nil)
	env.OnActivity("PublishTurnResultActivity", mock.Anything, mock.Anything).Return(nil)
	return env
}

func recoveryAnswer() LLMChatResult {
	return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}}}}
}

func signalRecoveryTurns(env *testsuite.TestWorkflowEnvironment, params RunAgentParams) {
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(PersistentInputSignal, PersistentInput{InputID: "one", Message: "first"}) }, time.Millisecond)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(PersistentInputSignal, PersistentInput{InputID: "two", Message: "second"}) }, 3*time.Second)
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, 6*time.Second)
	env.ExecuteWorkflow(PersistentRunWorkflow, params)
}

func TestPersistentTurnRetriesRecoveredPrimaryWithoutRepeatingTools(t *testing.T) {
	env := primaryRecoveryEnvironment()
	var routes []string
	primary, backup, tools := 0, 0, 0
	env.OnActivity("CallToolActivity", mock.Anything, mock.Anything).Return(func(context.Context, CallToolInput) (CallToolResult, error) {
		tools++
		return CallToolResult{Content: "completed lookup"}, nil
	})
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if in.Backup {
			routes = append(routes, "backup")
			backup++
			if backup == 1 {
				result := recoveryAnswer()
				result.Response.Choices[0].Message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "lookup-1", Type: "function", Function: llm.FunctionCall{Name: "lookup", Arguments: "{}"}}}}
				return result, nil
			}
			return recoveryAnswer(), nil
		}
		routes = append(routes, "primary")
		primary++
		if in.FailoverReason != "" || in.Request.Model != "primary" || in.ProviderID != "primary-provider" {
			t.Errorf("primary inherited stale backup routing: %+v", in)
		}
		if primary <= 2 {
			return LLMChatResult{}, fmt.Errorf("primary unavailable")
		}
		found := false
		for _, message := range in.Request.Messages {
			if message.Role == "tool" && message.Content == "completed lookup" {
				found = true
			}
		}
		if !found {
			t.Error("new turn lost completed tool history")
		}
		return recoveryAnswer(), nil
	})
	signalRecoveryTurns(env, RunAgentParams{AgentID: "a", BackupActive: true, BackupReason: "inherited outage"})
	want := []string{"primary", "primary", "backup", "backup", "primary"}
	if !reflect.DeepEqual(routes, want) || tools != 1 {
		t.Fatalf("routes=%v tools=%d, want %v and one tool execution", routes, tools, want)
	}
}

func TestPersistentTurnRetriesPrimaryAfterBothProvidersFail(t *testing.T) {
	env := primaryRecoveryEnvironment()
	primary, backup := 0, 0
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if in.Backup {
			backup++
			return LLMChatResult{}, fmt.Errorf("backup unavailable")
		}
		primary++
		if primary <= 2 {
			return LLMChatResult{}, fmt.Errorf("primary unavailable")
		}
		return recoveryAnswer(), nil
	})
	signalRecoveryTurns(env, RunAgentParams{AgentID: "a"})
	if primary != 3 || backup != 2 {
		t.Fatalf("primary=%d backup=%d", primary, backup)
	}
}

func TestPersistentNewInputUpgradesAfterLegacyTurnInSameExecution(t *testing.T) {
	env := primaryRecoveryEnvironment()
	env.OnGetVersion(persistentPrimaryRecoveryChangeID+"0", workflow.DefaultVersion, workflow.Version(1)).Return(workflow.DefaultVersion)
	var routes []bool
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		routes = append(routes, in.Backup)
		if !in.Backup && in.FailoverReason != "" {
			t.Error("new input retained legacy failure reason")
		}
		return recoveryAnswer(), nil
	})
	signalRecoveryTurns(env, RunAgentParams{AgentID: "a", BackupActive: true, BackupReason: "legacy outage"})
	if !reflect.DeepEqual(routes, []bool{true, false}) {
		t.Fatalf("legacy/new input routes=%v, want backup then primary", routes)
	}
}

func TestPersistentRolloverClearsBackupAndKeepsConversationState(t *testing.T) {
	env := primaryRecoveryEnvironment()
	primary := 0
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		if in.Backup {
			t.Error("rollover input inherited backup")
		}
		primary++
		return recoveryAnswer(), nil
	})
	var inputs []PersistentInput
	for i := 0; i < persistentRolloverTurns+1; i++ {
		inputs = append(inputs, PersistentInput{InputID: fmt.Sprintf("input-%d", i), Message: "hello"})
	}
	env.ExecuteWorkflow(PersistentRunWorkflow, RunAgentParams{AgentID: "a", BackupActive: true, BackupReason: "old outage", PendingInputs: inputs})
	var rollover *workflow.ContinueAsNewError
	if !errors.As(env.GetWorkflowError(), &rollover) {
		t.Fatalf("expected Continue-As-New, got %v", env.GetWorkflowError())
	}
	var next RunAgentParams
	if err := converter.GetDefaultDataConverter().FromPayloads(rollover.Input, &next); err != nil {
		t.Fatal(err)
	}
	if next.BackupActive || next.BackupReason != "" || primary != persistentRolloverTurns {
		t.Fatalf("stale rollover routing: %+v primary=%d", next, primary)
	}
	if len(next.History) == 0 || len(next.SeenInputIDs) != persistentRolloverTurns || len(next.PendingInputs) != 1 || next.PendingInputs[0].InputID != fmt.Sprintf("input-%d", persistentRolloverTurns) {
		t.Fatalf("rollover lost conversation/dedup/pending inputs: %+v", next)
	}
}
