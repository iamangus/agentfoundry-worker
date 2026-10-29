package temporal

import (
	"context"
	"testing"
	"time"

	"github.com/angoo/agentfoundry-worker/internal/config"
	"github.com/angoo/agentfoundry-worker/internal/llm"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
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
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).Return(func(_ context.Context, input LLMChatInput) (LLMChatResult, error) {
		if input.InputID != "input-1" {
			t.Errorf("missing turn correlation: %q", input.InputID)
		}
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

func TestPersistentWorkflowSteersBeforeFinalResponseAndRejectsLateUpdate(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(PersistentRunWorkflow)
	env.RegisterActivity(&Activities{})
	def := &config.Definition{AgentID: "agent-1", Name: "assistant", Model: "test-model", MaxTurns: 1}
	env.OnActivity("ResolveAgentActivity", mock.Anything, mock.Anything).Return(ResolveAgentResult{Definition: def}, nil)
	env.OnActivity("BuildToolDefsActivity", mock.Anything, mock.Anything).Return(BuildToolDefsResult{}, nil)
	llmCalls := 0
	env.OnActivity("LLMChatActivity", mock.Anything, mock.Anything).After(20 * time.Millisecond).Return(func(_ context.Context, in LLMChatInput) (LLMChatResult, error) {
		llmCalls++
		content := "stale answer"
		if llmCalls == 2 {
			content = "revised answer"
			found := false
			for _, message := range in.Request.Messages {
				if message.Role == "user" && message.Content == "[Additional input from web received while you were working]\nactually stop" {
					found = true
				}
			}
			if !found {
				t.Errorf("second inference did not receive the follow-up: %+v", in.Request.Messages)
			}
		}
		return LLMChatResult{Response: &llm.ChatResponse{Choices: []llm.Choice{{Index: 0, Message: llm.Message{Role: "assistant", Content: content}}}}}, nil
	})
	var events []PublishTurnResultInput
	env.OnActivity("PublishTurnResultActivity", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		events = append(events, args.Get(1).(PublishTurnResultInput))
	}).Return(nil)
	accepted, lateAccepted := false, true
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(PersistentInputSignal, PersistentInput{InputID: "first", Message: "hello"})
	}, time.Millisecond)
	sendFollowUp := func() {
		env.UpdateWorkflow(PersistentSteerUpdate, "follow-up", &testsuite.TestUpdateCallback{
			OnAccept: func() {},
			OnReject: func(err error) { t.Errorf("steering update rejected: %v", err) },
			OnComplete: func(result interface{}, err error) {
				if err != nil {
					t.Error(err)
					return
				}
				accepted, _ = result.(bool)
			},
		}, PersistentInput{InputID: "follow-up", Message: "actually stop", Metadata: map[string]string{"source": "web"}})
	}
	// Tie steering to inference actually starting, rather than to a wall-clock
	// offset from workflow startup. Resolve/build scheduling can take longer
	// under -race, otherwise the follow-up legitimately precedes inference.
	followUpScheduled := false
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		if info.ActivityType.Name == "LLMChatActivity" && !followUpScheduled {
			followUpScheduled = true
			env.RegisterDelayedCallback(sendFollowUp, time.Millisecond)
		}
	})
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(PersistentSteerUpdate, "too-late", &testsuite.TestUpdateCallback{
			OnAccept: func() {},
			OnReject: func(err error) { t.Errorf("late update rejected: %v", err) },
			OnComplete: func(result interface{}, err error) {
				if err != nil {
					t.Error(err)
					return
				}
				lateAccepted, _ = result.(bool)
			},
		}, PersistentInput{InputID: "too-late", Message: "too late", Metadata: map[string]string{"source": "web"}})
	}, 500*time.Millisecond)
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Second)
	env.ExecuteWorkflow(PersistentRunWorkflow, RunAgentParams{AgentID: "agent-1", StreamID: "run-1"})
	if !accepted || lateAccepted || llmCalls != 2 {
		t.Fatalf("steering result: accepted=%v late=%v model_calls=%d", accepted, lateAccepted, llmCalls)
	}
	if len(events) != 4 || events[0].Type != "reset" || events[1].Type != "input_processed" || events[1].InputID != "first" || events[2].Type != "input_processed" || events[2].InputID != "follow-up" || events[3].Type != "turn_done" || events[3].InputID != "first" || events[3].Data != "revised answer" {
		t.Fatalf("unexpected steering receipts: %+v", events)
	}
}
