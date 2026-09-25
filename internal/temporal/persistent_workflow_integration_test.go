package temporal

import (
	"context"
	"testing"
	"time"

	"github.com/angoo/agentfoundry-worker/internal/config"
	"github.com/angoo/agentfoundry-worker/internal/llm"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestPersistentWorkflowAcknowledgesInputAndIgnoresDuplicateSignal(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PersistentRunWorkflow)
	env.RegisterActivity(&Activities{})
	def := &config.Definition{AgentID: "agent-1", Name: "assistant", Model: "test-model", SystemPrompt: "Help."}
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: def}, nil)
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{}, nil)
	llmCalls := 0
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, _ LLMChatInput) (LLMChatResult, error) {
		llmCalls++
		return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Index: 0, Message: llm.Message{Role: "assistant", Content: "answer"}, FinishReason: "stop"}}}}, nil
	})
	var events []PublishTurnResultInput
	env.OnActivity("PublishTurnResultActivity", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		events = append(events, args.Get(1).(PublishTurnResultInput))
	}).Return(nil)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(PersistentInputSignal, PersistentInput{InputID: "input-1", Message: "hello"})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(PersistentInputSignal, PersistentInput{InputID: "input-1", Message: "hello"})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Second)
	env.ExecuteWorkflow(PersistentRunWorkflow, RunAgentParams{AgentID: "agent-1", StreamID: "run-1"})
	if llmCalls != 1 {
		t.Fatalf("duplicate input caused %d model calls", llmCalls)
	}
	if len(events) != 2 || events[0].Type != "input_processed" || events[0].InputID != "input-1" || events[1].Type != "turn_done" || events[1].InputID != "input-1" || events[1].Data != "answer" {
		t.Fatalf("unexpected turn receipts: %+v", events)
	}
}
