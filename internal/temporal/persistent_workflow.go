package temporal

import (
	"fmt"

	"go.temporal.io/sdk/workflow"

	"github.com/angoo/agentfoundry-worker/internal/llm"
)

// PersistentRunWorkflow receives durable input signals and keeps one agent
// history alive. Signals are drained by runAgentWorkflow only at LLM
// boundaries, after any preceding tool result has been recorded.
func PersistentRunWorkflow(ctx workflow.Context, params RunAgentParams) error {
	logger := workflow.GetLogger(ctx)
	inputs := workflow.GetSignalChannel(ctx, PersistentInputSignal)
	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)
	seen := make(map[string]bool)
	history := append([]llm.Message(nil), params.History...)

	for {
		var input PersistentInput
		inputs.Receive(ctx, &input)
		if input.InputID == "" || seen[input.InputID] {
			continue
		}
		seen[input.InputID] = true
		turnParams := params
		turnParams.Message = input.Message
		turnParams.History = history
		var result RunAgentResult
		err := func() error {
			var runErr error
			result, runErr = runAgentWorkflow(ctx, turnParams, &persistentInbox{channel: inputs, seen: seen})
			return runErr
		}()
		if err != nil {
			logger.Error("persistent run turn failed", "input_id", input.InputID, "error", err)
			_ = workflow.ExecuteActivity(actCtx, (*Activities).PublishTurnResultActivity, PublishTurnResultInput{
				StreamID: params.StreamID, Type: "turn_error", Data: err.Error(),
			}).Get(ctx, nil)
			continue
		}
		history = result.History
		if err := workflow.ExecuteActivity(actCtx, (*Activities).PublishTurnResultActivity, PublishTurnResultInput{
			StreamID: params.StreamID, Type: "turn_done", Data: result.Response,
		}).Get(ctx, nil); err != nil {
			return fmt.Errorf("publish turn result: %w", err)
		}
	}
}
