package temporal

import (
	"encoding/json"
	"fmt"

	"go.temporal.io/sdk/workflow"

	"github.com/angoo/agentfoundry-worker/internal/llm"
)

const persistentRolloverTurns = 20
const persistentHistoryEventLimit = 5000
const persistentSeenLimit = 1024
const persistentHistoryMessages = 120
const persistentHistoryBytes = 256 * 1024
const PersistentSteerUpdate = "persistent-steer"

// PersistentRunWorkflow receives durable input signals and keeps one agent
// history alive. Signals are drained by runAgentWorkflow only at LLM
// boundaries, after any preceding tool result has been recorded.
func PersistentRunWorkflow(ctx workflow.Context, params RunAgentParams) error {
	logger := workflow.GetLogger(ctx)
	inputs := workflow.GetSignalChannel(ctx, PersistentInputSignal)
	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)
	seen := make(map[string]bool)
	seenOrder := append([]string(nil), params.SeenInputIDs...)
	for _, id := range seenOrder {
		seen[id] = true
	}
	steer := &persistentSteering{seen: seen}
	if err := workflow.SetUpdateHandler(ctx, PersistentSteerUpdate, func(_ workflow.Context, input PersistentInput) (bool, error) {
		return steer.accept(input), nil
	}); err != nil {
		return fmt.Errorf("register steering update: %w", err)
	}
	history := append([]llm.Message(nil), params.History...)
	pending := append([]PersistentInput(nil), params.PendingInputs...)
	turns := 0

	for {
		var input PersistentInput
		if len(pending) > 0 {
			input, pending = pending[0], pending[1:]
		} else {
			inputs.Receive(ctx, &input)
		}
		if input.InputID == "" || seen[input.InputID] {
			continue
		}
		seen[input.InputID] = true
		seenOrder = append(seenOrder, input.InputID)
		turnParams := params
		turnParams.Message = input.Message
		turnParams.History = history
		var result RunAgentResult
		processed := []string{input.InputID}
		steer.accepting = true
		steer.turnInputID = input.InputID
		steer.onSeen = func(id string) {
			seenOrder = append(seenOrder, id)
			processed = append(processed, id)
		}
		err := func() error {
			var runErr error
			result, runErr = runAgentWorkflow(ctx, turnParams, &persistentInbox{channel: inputs, seen: seen, steer: steer, onSeen: func(id string) {
				seenOrder = append(seenOrder, id)
				processed = append(processed, id)
			}})
			return runErr
		}()
		steer.accepting = false
		if result.BackupActive {
			params.BackupActive, params.BackupReason = true, result.BackupReason
		}
		ackType := "input_processed"
		if err != nil {
			ackType = "input_failed"
		}
		for _, id := range processed {
			if ackErr := workflow.ExecuteActivity(actCtx, (*Activities).PublishTurnResultActivity, PublishTurnResultInput{
				StreamID: params.StreamID, InputID: id, Type: ackType,
			}).Get(ctx, nil); ackErr != nil {
				return fmt.Errorf("acknowledge input %s: %w", id, ackErr)
			}
		}
		if err != nil {
			logger.Error("persistent run turn failed", "input_id", input.InputID, "error", err)
			if publishErr := workflow.ExecuteActivity(actCtx, (*Activities).PublishTurnResultActivity, PublishTurnResultInput{
				StreamID: params.StreamID, InputID: input.InputID, Type: "turn_error", Data: err.Error(),
			}).Get(ctx, nil); publishErr != nil {
				return fmt.Errorf("publish turn error: %w", publishErr)
			}
		} else {
			history = result.History
			if err := workflow.ExecuteActivity(actCtx, (*Activities).PublishTurnResultActivity, PublishTurnResultInput{
				StreamID: params.StreamID, InputID: input.InputID, Type: "turn_done", Data: result.Response,
			}).Get(ctx, nil); err != nil {
				return fmt.Errorf("publish turn result: %w", err)
			}
		}
		turns++
		if turns >= persistentRolloverTurns || workflow.GetInfo(ctx).GetCurrentHistoryLength() >= persistentHistoryEventLimit {
			for {
				var queued PersistentInput
				if !inputs.ReceiveAsync(&queued) {
					break
				}
				pending = append(pending, queued)
			}
			params.History = trimPersistentHistory(history)
			if len(seenOrder) > persistentSeenLimit {
				seenOrder = seenOrder[len(seenOrder)-persistentSeenLimit:]
			}
			params.SeenInputIDs = seenOrder
			params.PendingInputs = pending
			return workflow.NewContinueAsNewError(ctx, PersistentRunWorkflow, params)
		}
	}
}

func trimPersistentHistory(history []llm.Message) []llm.Message {
	start, size := len(history), 0
	for start > 0 && len(history)-start < persistentHistoryMessages {
		encoded, _ := json.Marshal(history[start-1])
		next := len(encoded)
		if start < len(history) && size+next > persistentHistoryBytes {
			break
		}
		start--
		size += next
	}
	for i := start; i < len(history); i++ {
		if history[i].Role == "user" {
			start = i
			break
		}
	}
	return append([]llm.Message(nil), history[start:]...)
}
