package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Default base URL — OpenRouter is the default for backward compatibility.
const defaultBaseURL = "https://openrouter.ai/api/v1"

// Client is the interface for LLM providers.
type Client interface {
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	ChatCompletionStream(ctx context.Context, req *ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error)
	SupportsSchemaValidation() bool
}

// ClientConfig holds configuration for an OpenAI-compatible LLM client.
type ClientConfig struct {
	BaseURL string

	APIKey string

	DefaultModel string

	Headers map[string]string

	SchemaValidation bool
}

// OpenAIClient implements Client for any OpenAI-compatible API.
type OpenAIClient struct {
	baseURL          string
	apiKey           string
	defaultModel     string
	headers          map[string]string
	httpClient       *http.Client
	schemaValidation bool
}

// NewClient creates a new OpenAI-compatible LLM client.
func NewClient(cfg ClientConfig) *OpenAIClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &OpenAIClient{
		baseURL:          baseURL,
		apiKey:           cfg.APIKey,
		defaultModel:     cfg.DefaultModel,
		headers:          cfg.Headers,
		httpClient:       &http.Client{Timeout: 120 * time.Second},
		schemaValidation: cfg.SchemaValidation,
	}
}

func (c *OpenAIClient) SupportsSchemaValidation() bool {
	return c.schemaValidation
}

// ResponseFormat instructs the model to produce output in a specific format.
type ResponseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

// JSONSchema is the json_schema block within a ResponseFormat.
// It mirrors the OpenAI structured outputs format exactly.
type JSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

// ChatRequest represents a chat completion request.
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Tools          []ToolDef       `json:"tools,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// Message represents a chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"` // string or nil
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolDef represents a tool definition for the LLM.
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef represents a function definition within a tool.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall represents a tool call made by the LLM.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
	Index    int          `json:"index,omitempty"`
}

// FunctionCall represents the function portion of a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponse represents a chat completion response.
type ChatResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a response choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage represents token usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type StreamChunk struct {
	ID      string         `json:"id"`
	Choices []StreamChoice `json:"choices"`
}

type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type StreamDelta struct {
	Role      string     `json:"role,omitempty"`
	Content   *string    `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

func isEmptyResponse(resp *ChatResponse) bool {
	if len(resp.Choices) == 0 {
		return true
	}
	for _, c := range resp.Choices {
		if c.FinishReason == "length" {
			return true
		}
		if len(c.Message.ToolCalls) > 0 {
			return false
		}
		if c.Message.Content != nil {
			if s, ok := c.Message.Content.(string); ok && s != "" {
				return false
			}
		}
	}
	return true
}

func isRetryableHTTP(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// ChatCompletion sends a chat completion request to the configured API endpoint.
// Transient errors (429, 500, 502, 503, 504) and network errors are retried
// with exponential backoff: 1s, 3s, 7s, then every 10s up to maxRetries.
func (c *OpenAIClient) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.defaultModel
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	backoffSchedule := []time.Duration{
		1 * time.Second,
		3 * time.Second,
		7 * time.Second,
	}
	const defaultBackoff = 10 * time.Second
	const maxRetries = 10

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			var wait time.Duration
			if attempt-1 < len(backoffSchedule) {
				wait = backoffSchedule[attempt-1]
			} else {
				wait = defaultBackoff
			}
			slog.Warn("LLM request failed, retrying",
				"attempt", attempt,
				"wait", wait,
				"error", lastErr,
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		for k, v := range c.headers {
			httpReq.Header.Set(k, v)
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read response: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var chatResp ChatResponse
			if err := json.Unmarshal(respBody, &chatResp); err != nil {
				return nil, fmt.Errorf("parse response: %w", err)
			}
			if isEmptyResponse(&chatResp) {
				lastErr = fmt.Errorf("LLM returned empty response (no content, no tool calls, or finish_reason=length)")
				continue
			}
			return &chatResp, nil
		}

		lastErr = fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(respBody))
		if !isRetryableHTTP(resp.StatusCode) {
			return nil, lastErr
		}
	}

	return nil, fmt.Errorf("LLM request failed after %d retries: %w", maxRetries, lastErr)
}

func (c *OpenAIClient) ChatCompletionStream(ctx context.Context, req *ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.defaultModel
	}

	streamReq := &struct {
		*ChatRequest
		Stream bool `json:"stream"`
	}{ChatRequest: req, Stream: true}

	body, err := json.Marshal(streamReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	streamClient := &http.Client{Timeout: 5 * time.Minute}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(respBody))
	}

	var accumulatedContent string
	var accumulatedToolCalls []ToolCall
	var role string
	var chunkID string

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Warn("failed to parse stream chunk", "error", err, "data", data)
			continue
		}

		if chunk.ID != "" {
			chunkID = chunk.ID
		}

		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.Delta.Role != "" {
				role = choice.Delta.Role
			}
			if choice.Delta.Content != nil {
				accumulatedContent += *choice.Delta.Content
			}
			for _, tc := range choice.Delta.ToolCalls {
				idx := tc.Index
				for len(accumulatedToolCalls) <= idx {
					accumulatedToolCalls = append(accumulatedToolCalls, ToolCall{})
				}
				// Guard Type against empty-string overwrite: only the first
				// delta for a given tool-call index carries "type":"function";
				// subsequent argument-streaming deltas omit it. Clobbering
				// Type back to "" caused Azure to silently drop the tool_call,
				// orphaning the subsequent {role:"tool", tool_call_id:...}
				// messages and producing a 400 on the next turn.
				if tc.Type != "" {
					accumulatedToolCalls[idx].Type = tc.Type
				}
				if tc.ID != "" {
					accumulatedToolCalls[idx].ID = tc.ID
				}
				// Guard Function.Name symmetrically against providers that
				// split the name across multiple deltas with empty trailing
				// fragments.
				if tc.Function.Name != "" {
					accumulatedToolCalls[idx].Function.Name += tc.Function.Name
				}
				// Arguments are intentionally append-only (streamed).
				accumulatedToolCalls[idx].Function.Arguments += tc.Function.Arguments
				accumulatedToolCalls[idx].Index = tc.Index
			}
		}

		if onChunk != nil {
			onChunk(chunk)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("stream scanner error", "error", err)
	}

	// Post-stream normalization: repair any provider quirks that would
	// otherwise produce a malformed assistant message on the next turn.
	//
	// 1. Default role to "assistant" if the provider never sent a role delta
	//    (some providers omit it on tool-call-only responses).
	if role == "" {
		role = "assistant"
	}
	// 2. Repair any tool_call that ended up with empty Type or ID. Empty Type
	//    causes Azure/OpenAI to silently drop the tool_call, orphaning the
	//    subsequent {role:"tool", tool_call_id:...} messages. Empty ID makes
	//    it impossible to pair the tool output with the call.
	for i := range accumulatedToolCalls {
		if accumulatedToolCalls[i].Type == "" {
			accumulatedToolCalls[i].Type = "function"
		}
		if accumulatedToolCalls[i].ID == "" {
			accumulatedToolCalls[i].ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
			slog.Warn("streaming accumulator: synthesized missing tool_call ID",
				"synthetic_id", accumulatedToolCalls[i].ID,
				"index", i,
				"function", accumulatedToolCalls[i].Function.Name)
		}
	}
	// 3. For tool-call-only assistant messages, an empty-string content can
	//    cause strict providers to reject the message. Coerce to nil so the
	//    omit-empty tag drops the field entirely.
	finalContent := any(accumulatedContent)
	if accumulatedContent == "" && len(accumulatedToolCalls) > 0 {
		finalContent = nil
	}

	result := &ChatResponse{
		ID: chunkID,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:      role,
					Content:   finalContent,
					ToolCalls: accumulatedToolCalls,
				},
				FinishReason: "stop",
			},
		},
	}

	return result, nil
}

// StripCodeFences extracts raw JSON from an LLM text response.
// It handles three common wrapping patterns:
//  1. Markdown code fences: ```json\n{...}\n```
//  2. Preamble text before JSON: "Here is the result: {...}"
//  3. A combination of both.
//
// If the input is already valid JSON (starts with '{' or '[') it is returned
// unchanged. If no JSON object or array can be located, the trimmed input is
// returned as-is.
func StripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}

	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s[3:], "\n"); idx >= 0 {
			s = s[3+idx+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}

	return extractBalancedJSON(s)
}

func extractBalancedJSON(s string) string {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '{' || s[i] == '[' {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}

	open, close := s[start], byte('}')
	if open == '[' {
		close = ']'
	}

	depth := 0
	inStr := false
	escape := false
	end := -1
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inStr {
			escape = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == open {
			depth++
		} else if c == close {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}

	if end < 0 {
		return s[start:]
	}
	return s[start : end+1]
}

// IsValidJSON checks whether s is valid JSON. It returns nil if so, or an
// error describing the parse failure.
func IsValidJSON(s string) error {
	var v any
	return json.Unmarshal([]byte(s), &v)
}

// ValidateAgainstSchema validates content (a JSON string) against the given
// JSON Schema. It returns nil on success or a descriptive error on failure.
func ValidateAgainstSchema(content string, schema json.RawMessage) error {
	compiler := jsonschema.NewCompiler()
	schemaID := "schema.json"
	if err := compiler.AddResource(schemaID, strings.NewReader(string(schema))); err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	sch := compiler.MustCompile(schemaID)

	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := sch.Validate(v); err != nil {
		return err
	}
	return nil
}

// NormalizeMessages repairs common provider-induced malformations in a chat
// message slice before it is sent to an OpenAI-compatible endpoint. It is
// idempotent and safe to call on every turn.
//
// Responsibilities:
//  1. Force empty assistant tool_call.Type to "function" (Azure silently drops
//     tool_calls with empty Type, orphaning subsequent tool messages).
//  2. Assign a synthetic ID to any assistant tool_call with an empty ID, and
//     expose it via the known-id set so subsequent tool messages can reference
//     it.
//  3. Drop any tool message whose ToolCallID is not declared by a preceding
//     assistant message's tool_calls. This is the specific guarantee that
//     prevents "No tool call found for function call output with call_id ..."
//     400 errors from Azure/OpenAI, regardless of upstream cause.
//  4. Default an empty assistant Role to "assistant".
//  5. For tool-call-only assistant messages, coerce empty-string Content to
//     nil so the json:"content,omitempty" tag drops the field entirely.
//  6. Dedupe consecutive assistant messages that declare the exact same set
//     of tool_call IDs as the immediately preceding one (guards against
//     retry/replay duplication).
//
// All repairs log via slog.Warn so future provider quirks remain diagnosable.
func NormalizeMessages(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}

	out := make([]Message, 0, len(msgs))
	knownToolCallIDs := make(map[string]struct{})
	var prevAssistantToolCallIDs []string

	for _, m := range msgs {
		// (4) Default Role: an empty Role almost always means a
		// tool-call-bearing (or text-only) assistant message
		// constructed upstream without an explicit role — e.g. the
		// streaming accumulator when the provider never sent a role
		// delta. Treat any empty-role message as "assistant" and
		// apply the assistant branch's repairs. This also makes the
		// switch below hit the assistant case for empty-role messages.
		if m.Role == "" {
			m.Role = "assistant"
		}

		switch m.Role {
		case "assistant":

			// (1) + (2) Repair each tool_call.
			var thisAssistantToolCallIDs []string
			for i := range m.ToolCalls {
				if m.ToolCalls[i].Type == "" {
					m.ToolCalls[i].Type = "function"
				}
				if m.ToolCalls[i].ID == "" {
					syntheticID := fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
					slog.Warn("NormalizeMessages: synthesized missing tool_call ID",
						"synthetic_id", syntheticID,
						"index", i,
						"function", m.ToolCalls[i].Function.Name)
					m.ToolCalls[i].ID = syntheticID
				}
				knownToolCallIDs[m.ToolCalls[i].ID] = struct{}{}
				thisAssistantToolCallIDs = append(thisAssistantToolCallIDs, m.ToolCalls[i].ID)
			}

			// (6) Dedupe: drop consecutive assistant messages declaring the
			//     exact same set of tool_call IDs as the previous one.
			if len(thisAssistantToolCallIDs) > 0 && len(prevAssistantToolCallIDs) > 0 {
				same := len(thisAssistantToolCallIDs) == len(prevAssistantToolCallIDs)
				for i := range thisAssistantToolCallIDs {
					if !same {
						break
					}
					if thisAssistantToolCallIDs[i] != prevAssistantToolCallIDs[i] {
						same = false
					}
				}
				if same {
					slog.Warn("NormalizeMessages: dropping duplicate assistant tool_call replay",
						"tool_call_ids", thisAssistantToolCallIDs)
					// Don't touch knownToolCallIDs; the first occurrence is
					// already registered.
					continue
				}
			}

			// (5) Coerce empty-string Content to nil for tool-call-only
			//     assistant messages.
			if len(m.ToolCalls) > 0 {
				if s, ok := m.Content.(string); ok && s == "" {
					m.Content = nil
				}
			}

			out = append(out, m)
			prevAssistantToolCallIDs = thisAssistantToolCallIDs

		case "tool":
			// (3) Drop orphaned tool messages. A tool message is orphaned if
			// its ToolCallID is not declared by any preceding assistant
			// message. This is the guarantee that prevents the
			// "No tool call found for function call output with call_id ..."
			// 400 from Azure/OpenAI.
			if _, ok := knownToolCallIDs[m.ToolCallID]; !ok {
				slog.Warn("NormalizeMessages: dropping orphaned tool message",
					"tool_call_id", m.ToolCallID)
				continue
			}
			out = append(out, m)
			prevAssistantToolCallIDs = nil

		default:
			out = append(out, m)
			prevAssistantToolCallIDs = nil
		}
	}

	return out
}
