package temporal

import (
	"encoding/json"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/angoo/agentfoundry-worker/internal/llm"
	"github.com/angoo/agentfoundry-worker/internal/memory"
)

var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 2 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 5,
	},
}

var llmActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 5 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts:        10,
		InitialInterval:        time.Second,
		BackoffCoefficient:     2.0,
		MaximumInterval:        10 * time.Second,
		NonRetryableErrorTypes: []string{"NonRetryable"},
	},
}

func RunAgentWorkflow(ctx workflow.Context, params RunAgentParams) (RunAgentResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("starting agent workflow", "agent", params.AgentName)

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)

	// 1. Resolve the agent definition.
	var resolveResult ResolveAgentResult
	err := workflow.ExecuteActivity(actCtx, (*Activities).ResolveAgentActivity, ResolveAgentInput{
		AgentID: params.AgentID,
	}).Get(ctx, &resolveResult)
	if err != nil {
		return RunAgentResult{}, fmt.Errorf("resolve agent: %w", err)
	}
	def := resolveResult.Definition

	// 2. Build the tool set (LLM tool definitions + routing table).
	var toolDefsResult BuildToolDefsResult
	err = workflow.ExecuteActivity(actCtx, (*Activities).BuildToolDefsActivity, BuildToolDefsInput{
		Definition: def,
	}).Get(ctx, &toolDefsResult)
	if err != nil {
		return RunAgentResult{}, fmt.Errorf("build tool defs: %w", err)
	}

	routeByLLMName := make(map[string]ToolRoute, len(toolDefsResult.ToolRoutes))
	for _, r := range toolDefsResult.ToolRoutes {
		routeByLLMName[r.LLMName] = r
	}

	// 3. Determine structured output / response format.
	so := params.ResponseSchema
	if so == nil {
		so = def.StructuredOutput
	}

	supportsSchema := params.LLMConfig != nil && params.LLMConfig.SchemaValidation

	// 4. Build initial messages: system prompt + history + user message.
	systemPrompt := def.SystemPrompt

	// 4a. Memory search: invoke memory search agent for queries, then search Graphiti.
	if params.MemoryEnabled && params.MemorySearchAgentID != "" {
		queries, err := invokeMemorySearchAgent(ctx, params, def.Name)
		if err != nil {
			logger.Warn("memory search agent failed", "error", err)
		} else if len(queries) > 0 {
			var memResult SearchMemoryResult
			err = workflow.ExecuteActivity(actCtx, (*Activities).SearchMemoryActivity, SearchMemoryInput{
				GroupID: def.AgentID,
				Queries: queries,
			}).Get(ctx, &memResult)
			if err != nil {
				logger.Warn("memory search failed", "agent", def.Name, "error", err)
			} else if len(memResult.Facts) > 0 {
				var factLines string
				for _, f := range memResult.Facts {
					factLines += "\n- " + f
				}
				systemPrompt += fmt.Sprintf("\n\n[Relevant context from past interactions]\n%s", factLines)
			}
		}
	}

	if so != nil && !supportsSchema {
		systemPrompt += fmt.Sprintf(
			"\n\nYou must respond with ONLY valid JSON matching this schema:\n%s",
			string(so.Schema),
		)
	}

	messages := make([]llm.Message, 0, 2+len(params.History))
	messages = append(messages, llm.Message{Role: "system", Content: systemPrompt})
	messages = append(messages, params.History...)
	messages = append(messages, llm.Message{Role: "user", Content: params.Message})

	maxTurns := def.MaxTurns
	if maxTurns == 0 {
		maxTurns = 10
	}

	llmCtx := workflow.WithActivityOptions(ctx, llmActivityOptions)

	// 5. Multi-turn loop.
	for turn := 0; turn < maxTurns; turn++ {
		logger.Info("agent turn", "agent", def.Name, "turn", turn+1)

		req := &llm.ChatRequest{
			Model:    def.Model,
			Messages: messages,
		}
		if len(toolDefsResult.ToolDefs) > 0 {
			req.Tools = toolDefsResult.ToolDefs
		}
		if so != nil && supportsSchema {
			req.ResponseFormat = &llm.ResponseFormat{
				Type: "json_schema",
				JSONSchema: &llm.JSONSchema{
					Name:   so.Name,
					Schema: so.Schema,
					Strict: so.Strict,
				},
			}
		} else if so != nil {
			req.ResponseFormat = &llm.ResponseFormat{Type: "json_object"}
		} else if def.ForceJSON {
			req.ResponseFormat = &llm.ResponseFormat{Type: "json_object"}
		}

		var llmResult LLMChatResult
		err = workflow.ExecuteActivity(llmCtx, (*Activities).LLMChatActivity, LLMChatInput{
			Request:   req,
			StreamID:  params.StreamID,
			LLMConfig: params.LLMConfig,
			AgentID:   params.AgentID,
		}).
			Get(ctx, &llmResult)
		if err != nil {
			return RunAgentResult{}, fmt.Errorf("LLM call failed on turn %d: %w", turn+1, err)
		}

		resp := llmResult.Response
		if len(resp.Choices) == 0 {
			return RunAgentResult{}, fmt.Errorf("no choices in LLM response on turn %d", turn+1)
		}

		var assistantMsg llm.Message
		for _, c := range resp.Choices {
			if c.Index != 0 {
				continue
			}
			if assistantMsg.Role == "" {
				assistantMsg.Role = c.Message.Role
			}
			if assistantMsg.Content == nil {
				assistantMsg.Content = c.Message.Content
			}
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, c.Message.ToolCalls...)
		}
		messages = append(messages, assistantMsg)

		if len(assistantMsg.ToolCalls) == 0 {
			content, _ := assistantMsg.Content.(string)
			if so != nil || def.ForceJSON {
				content = llm.StripCodeFences(content)
			}

			if so != nil {
				if verr := llm.ValidateAgainstSchema(content, so.Schema); verr != nil {
					logger.Warn("LLM response failed schema validation, retrying",
						"agent", def.Name, "turn", turn+1, "error", verr)
					var retryMsg string
					if !supportsSchema {
						retryMsg = fmt.Sprintf(
							"Your previous response did not conform to the required JSON schema.\n\n"+
								"Validation error: %s\n\n"+
								"The JSON schema you must follow:\n%s\n\n"+
								"Please try again, returning ONLY valid JSON that matches this schema exactly.",
							verr, string(so.Schema),
						)
					} else {
						retryMsg = fmt.Sprintf(
							"Your previous response did not conform to the required JSON schema. "+
								"Validation error: %s\n\nPlease try again, returning ONLY valid JSON that matches the schema exactly.",
							verr,
						)
					}
					messages = append(messages, llm.Message{Role: "user", Content: retryMsg})
					continue
				}
			} else if def.ForceJSON {
				if jerr := llm.IsValidJSON(content); jerr != nil {
					logger.Warn("LLM response was not valid JSON, retrying",
						"agent", def.Name, "turn", turn+1, "error", jerr)
					messages = append(messages, llm.Message{
						Role: "user",
						Content: fmt.Sprintf(
							"Your previous response was not valid JSON. "+
								"Parse error: %s\n\nPlease try again, returning ONLY valid JSON.",
							jerr,
						),
					})
					continue
				}
			}

			logger.Info("agent workflow completed", "agent", def.Name, "turns", turn+1)
			finalResult := RunAgentResult{
				Response: content,
				History:  messages[1:],
			}

		if params.MemoryEnabled && params.MemoryIngestAgentID != "" {
			episodes, err := invokeMemoryIngestAgent(ctx, params, messages, def.Name)
			if err != nil {
				logger.Warn("memory ingest agent failed", "error", err)
			} else if len(episodes) > 0 {
				_ = workflow.ExecuteActivity(actCtx, (*Activities).IngestEpisodeActivity, IngestEpisodeInput{
					GroupID:  def.AgentID,
					Episodes: episodes,
				}).Get(ctx, nil)
			}
		}
			return finalResult, nil
		}

		type toolCallOutcome struct {
			toolCallID string
			content    string
		}

		outcomes := make([]toolCallOutcome, len(assistantMsg.ToolCalls))
		errors := make([]error, len(assistantMsg.ToolCalls))

		maxConcurrent := def.MaxConcurrentTools
		concurrency := maxConcurrent
		if concurrency == 0 {
			concurrency = len(assistantMsg.ToolCalls)
		}
		sem := workflow.NewSemaphore(ctx, int64(concurrency))

		wg := workflow.NewWaitGroup(ctx)
		for i, tc := range assistantMsg.ToolCalls {
			i, tc := i, tc
			wg.Add(1)
			workflow.Go(ctx, func(ctx workflow.Context) {
				defer wg.Done()
				sem.Acquire(ctx, 1)
				defer sem.Release(1)

				content, err := dispatchToolCall(ctx, tc, routeByLLMName, params)
				outcomes[i] = toolCallOutcome{toolCallID: tc.ID, content: content}
				errors[i] = err
			})
		}
		wg.Wait(ctx)

		for i, outcome := range outcomes {
			content := outcome.content
			if errors[i] != nil {
				logger.Warn("tool call failed", "agent", def.Name, "tool", assistantMsg.ToolCalls[i].Function.Name, "error", errors[i])
				content = fmt.Sprintf("Error: %s", errors[i].Error())
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    content,
				ToolCallID: outcome.toolCallID,
			})
		}
	}

	return RunAgentResult{}, fmt.Errorf("agent %s exceeded max turns (%d)", def.Name, maxTurns)
}

func dispatchToolCall(
	ctx workflow.Context,
	tc llm.ToolCall,
	routeByLLMName map[string]ToolRoute,
	params RunAgentParams,
) (string, error) {
	logger := workflow.GetLogger(ctx)

	route, ok := routeByLLMName[tc.Function.Name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}

	switch route.Kind {
	case ToolKindAgent:
		var agentInput struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &agentInput); err != nil {
			return "", fmt.Errorf("parse agent call input: %w", err)
		}
		logger.Info("dispatching sub-agent", "agent_id", route.AgentID, "input_len", len(agentInput.Message))

		childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			TaskQueue: TaskQueue,
			SearchAttributes: map[string]interface{}{
				"AgentName": route.AgentName,
				"RunType":   "direct",
			},
		})
		var childResult RunAgentResult
		err := workflow.ExecuteChildWorkflow(childCtx, RunAgentWorkflow, RunAgentParams{
			AgentID:   route.AgentID,
			AgentName: route.AgentName,
			Message:   agentInput.Message,
		}).Get(ctx, &childResult)
		if err != nil {
			return "", fmt.Errorf("sub-agent %s failed: %w", route.AgentID, err)
		}
		return childResult.Response, nil

	case ToolKindMCP:
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parse tool arguments: %w", err)
		}
		logger.Info("dispatching MCP tool", "server", route.ServerName, "tool", route.ToolName)

		actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)
		var result CallToolResult
		err := workflow.ExecuteActivity(actCtx, (*Activities).CallToolActivity, CallToolInput{
			ServerName: route.ServerName,
			ToolName:   route.ToolName,
			Arguments:  args,
			StreamID:   params.StreamID,
		}).Get(ctx, &result)
		if err != nil {
			return "", err
		}
		if result.IsError {
			return "", fmt.Errorf("tool returned error: %s", result.Content)
		}
		return result.Content, nil

	default:
		return "", fmt.Errorf("unknown tool kind %q for tool %s", route.Kind, tc.Function.Name)
	}
}

func invokeMemorySearchAgent(ctx workflow.Context, params RunAgentParams, agentName string) ([]string, error) {
	task := fmt.Sprintf(
		"Given the following user message and conversation history, generate search queries to retrieve relevant past facts from a knowledge graph.\n\n"+
			"User message: %s\n\n"+
			"Generate 1-5 diverse semantic search queries that would surface relevant memories about this user, their preferences, past interactions, and related topics.",
		params.Message,
	)

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var resolveResult ResolveAgentResult
	if err := workflow.ExecuteActivity(actCtx, (*Activities).ResolveAgentActivity, ResolveAgentInput{
		AgentID: params.MemorySearchAgentID,
	}).Get(ctx, &resolveResult); err != nil {
		return nil, err
	}
	childDisplayName := resolveResult.Definition.Name

	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		TaskQueue: TaskQueue,
		SearchAttributes: map[string]interface{}{
			"AgentName": childDisplayName,
			"RunType":   "direct",
		},
	})
	var childResult RunAgentResult
	err := workflow.ExecuteChildWorkflow(childCtx, RunAgentWorkflow, RunAgentParams{
		AgentID:        params.MemorySearchAgentID,
		AgentName:      childDisplayName,
		Message:        task,
		History:        params.History,
		MemoryEnabled:  false,
	}).Get(ctx, &childResult)
	if err != nil {
		return nil, err
	}

	var output MemorySearchAgentOutput
	if err := json.Unmarshal([]byte(childResult.Response), &output); err != nil {
		// Try cleaning code fences
		cleaned := llm.StripCodeFences(childResult.Response)
		if err2 := json.Unmarshal([]byte(cleaned), &output); err2 != nil {
			return nil, fmt.Errorf("parse memory search agent response: %w (original: %w)", err2, err)
		}
	}
	return output.Queries, nil
}

func invokeMemoryIngestAgent(ctx workflow.Context, params RunAgentParams, messages []llm.Message, agentName string) ([]memory.Episode, error) {
	var turnStr string
	for _, m := range messages {
		content, _ := m.Content.(string)
		if content == "" {
			continue
		}
		role := m.Role
		if role == "tool" {
			role = "tool_result"
		}
		turnStr += fmt.Sprintf("[%s] %s\n", role, content)
	}

	task := fmt.Sprintf(
		"Given the following conversation turn, extract distinct episodes to store in a knowledge graph.\n\n"+
			"Turn context:\n%s\n\n"+
			"Extract 0-5 distinct episodes from this turn. Each episode should be a self-contained piece of information that could be useful in future interactions.",
		turnStr,
	)

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var resolveResult ResolveAgentResult
	if err := workflow.ExecuteActivity(actCtx, (*Activities).ResolveAgentActivity, ResolveAgentInput{
		AgentID: params.MemoryIngestAgentID,
	}).Get(ctx, &resolveResult); err != nil {
		return nil, err
	}
	childDisplayName := resolveResult.Definition.Name

	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		TaskQueue: TaskQueue,
		SearchAttributes: map[string]interface{}{
			"AgentName": childDisplayName,
			"RunType":   "direct",
		},
	})
	var childResult RunAgentResult
	err := workflow.ExecuteChildWorkflow(childCtx, RunAgentWorkflow, RunAgentParams{
		AgentID:        params.MemoryIngestAgentID,
		AgentName:      childDisplayName,
		Message:        task,
		MemoryEnabled:  false,
	}).Get(ctx, &childResult)
	if err != nil {
		return nil, err
	}

	var output MemoryIngestAgentOutput
	if err := json.Unmarshal([]byte(childResult.Response), &output); err != nil {
		cleaned := llm.StripCodeFences(childResult.Response)
		if err2 := json.Unmarshal([]byte(cleaned), &output); err2 != nil {
			return nil, fmt.Errorf("parse memory ingest agent response: %w (original: %w)", err2, err)
		}
	}
	return output.Episodes, nil
}
