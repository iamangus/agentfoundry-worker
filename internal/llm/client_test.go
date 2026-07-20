package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// toolCallIDCounter ensures unique synthetic IDs across test runs that call
// NormalizeMessages (which uses time.Now().UnixNano() for synthetic IDs).
// Not used directly — kept here as documentation of test determinism notes.

func TestStripCodeFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bare JSON object",
			input: `{"tasks":[]}`,
			want:  `{"tasks":[]}`,
		},
		{
			name:  "bare JSON array",
			input: `[1,2,3]`,
			want:  `[1,2,3]`,
		},
		{
			name:  "json code fence",
			input: "```json\n{\"tasks\":[]}\n```",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "bare code fence without language",
			input: "```\n{\"tasks\":[]}\n```",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "preamble text before JSON",
			input: "Here are my findings: {\"tasks\":[]}",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "preamble with JSON keyword",
			input: "Here are my findings: JSON{\"tasks\":[]}",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "preamble with code fence",
			input: "Here are my findings:\n```json\n{\"tasks\":[]}\n```",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "leading whitespace",
			input: "  \n  {\"tasks\":[]}  \n  ",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace only",
			input: "   \n\t  ",
			want:  "",
		},
		{
			name:  "preamble with array",
			input: "The result is: [1, 2, 3]",
			want:  "[1, 2, 3]",
		},
		{
			name:  "nested braces in preamble",
			input: "Here is some {nested} and then {\"tasks\":[]}",
			want:  "{nested}",
		},
		{
			name:  "string with braces inside JSON",
			input: `{"reason": "used {curly braces}", "outcome": "ok"}`,
			want:  `{"reason": "used {curly braces}", "outcome": "ok"}`,
		},
		{
			name:  "escaped quotes inside JSON",
			input: `{"reason": "he said \"hello\"", "outcome": "ok"}`,
			want:  `{"reason": "he said \"hello\"", "outcome": "ok"}`,
		},
		{
			name:  "preamble with escaped quotes inside JSON",
			input: `Here is the result: {"reason": "he said \"hello\"", "outcome": "ok"}`,
			want:  `{"reason": "he said \"hello\"", "outcome": "ok"}`,
		},
		{
			name:  "trailing content after JSON object",
			input: `{"tasks":[]}<system-reminder>you are in build mode</system-reminder>`,
			want:  `{"tasks":[]}`,
		},
		{
			name:  "trailing content after JSON array",
			input: `[1,2,3]some trailing garbage`,
			want:  `[1,2,3]`,
		},
		{
			name:  "trailing content after code fence",
			input: "```json\n{\"tasks\":[]}\n```\n<system-reminder>noise</system-reminder>",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "bare JSON with trailing newline and text",
			input: "{\"tasks\":[]}\n\nI hope this helps!",
			want:  `{"tasks":[]}`,
		},
		{
			name:  "plain text no JSON",
			input: "just some text with no json at all",
			want:  "just some text with no json at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripCodeFences(tt.input)
			if got != tt.want {
				t.Errorf("StripCodeFences() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsValidJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid object", input: `{"key": "value"}`, wantErr: false},
		{name: "valid array", input: `[1, 2, 3]`, wantErr: false},
		{name: "valid string", input: `"hello"`, wantErr: false},
		{name: "valid number", input: `42`, wantErr: false},
		{name: "empty object", input: `{}`, wantErr: false},
		{name: "invalid JSON", input: `{not json}`, wantErr: true},
		{name: "empty string", input: ``, wantErr: true},
		{name: "plain text", input: `hello world`, wantErr: true},
		{name: "trailing comma", input: `{"a": 1,}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IsValidJSON(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsValidJSON(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateAgainstSchema(t *testing.T) {
	simpleSchema := json.RawMessage(`{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"properties": {
			"reason": {"type": "string"},
			"outcome": {"type": "string"}
		},
		"required": ["reason", "outcome"],
		"additionalProperties": false
	}`)

	tasksSchema := json.RawMessage(`{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"properties": {
			"tasks": {
				"type": "array",
				"items": {"type": "string"}
			}
		},
		"required": ["tasks"]
	}`)

	tests := []struct {
		name    string
		content string
		schema  json.RawMessage
		wantErr bool
	}{
		{
			name:    "valid against simple schema",
			content: `{"reason": "test", "outcome": "ok"}`,
			schema:  simpleSchema,
			wantErr: false,
		},
		{
			name:    "missing required field",
			content: `{"reason": "test"}`,
			schema:  simpleSchema,
			wantErr: true,
		},
		{
			name:    "wrong type for field",
			content: `{"reason": 123, "outcome": "ok"}`,
			schema:  simpleSchema,
			wantErr: true,
		},
		{
			name:    "extra properties rejected",
			content: `{"reason": "test", "outcome": "ok", "extra": true}`,
			schema:  simpleSchema,
			wantErr: true,
		},
		{
			name:    "valid tasks array with strings",
			content: `{"tasks": ["a", "b"]}`,
			schema:  tasksSchema,
			wantErr: false,
		},
		{
			name:    "tasks as objects instead of strings",
			content: `{"tasks": [{"id": "TASK-001", "title": "test"}]}`,
			schema:  tasksSchema,
			wantErr: true,
		},
		{
			name:    "invalid JSON content",
			content: `{not json}`,
			schema:  tasksSchema,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgainstSchema(tt.content, tt.schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAgainstSchema() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// --- Streaming accumulator tests (Fix 1) ---
//
// These reproduce the Azure streaming behavior that caused the
// "No tool call found for function call output with call_id ..."
// 400: only the first delta for a tool-call index carries type/id;
// subsequent argument-streaming deltas omit them. Before the fix,
// accumulatedToolCalls[idx].Type was clobbered back to "" on every
// delta, causing Azure to silently drop the tool_call on the next
// turn.

// writeSSEStream writes the given data chunks as SSE "data:" lines,
// followed by the [DONE] sentinel, to the provided ResponseWriter.
func writeSSEStream(w http.ResponseWriter, chunks []string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		panic("ResponseWriter does not implement http.Flusher")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, c := range chunks {
		w.Write([]byte("data: " + c + "\n"))
		flusher.Flush()
	}
	w.Write([]byte("data: [DONE]\n"))
	flusher.Flush()
}

func TestChatCompletionStream_ToolCallAccumulation(t *testing.T) {
	// Simulate Azure's multi-chunk streaming for a single tool call:
	// chunk A carries id/type/name; chunks B and C carry argument
	// fragments with NO type/id (the quirk that clobbered Type before).
	chunks := []string{
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"foo","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSEStream(w, chunks)
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	resp, err := c.ChatCompletionStream(t.Context(), &ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("ChatCompletionStream error: %v", err)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if msg.Role != "assistant" {
		t.Errorf("expected role=assistant, got %q", msg.Role)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("expected ID=call_abc, got %q", tc.ID)
	}
	if tc.Type != "function" {
		t.Errorf("expected Type=function (the bug we fixed), got %q", tc.Type)
	}
	if tc.Function.Name != "foo" {
		t.Errorf("expected Function.Name=foo, got %q", tc.Function.Name)
	}
	wantArgs := `{"x":1}`
	if tc.Function.Arguments != wantArgs {
		t.Errorf("expected Arguments=%q, got %q", wantArgs, tc.Function.Arguments)
	}
	// Tool-call-only assistant message should have nil Content, not "".
	if msg.Content != nil {
		if s, ok := msg.Content.(string); ok && s == "" {
			// OK — nil is preferred, but empty string is also acceptable.
		} else if ok {
			t.Errorf("expected Content=nil for tool-call-only message, got %q", s)
		}
	}
}

func TestChatCompletionStream_MultiToolCallParallel(t *testing.T) {
	// Two tool calls interleaved across deltas. Index 0 and 1 each
	// receive their id/type in separate chunks, then argument fragments.
	chunks := []string{
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"toolA","arguments":""}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"toolB","arguments":""}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1}"}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"b\":2}"}}]}}]}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSEStream(w, chunks)
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	resp, err := c.ChatCompletionStream(t.Context(), &ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("ChatCompletionStream error: %v", err)
	}

	if len(resp.Choices[0].Message.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(resp.Choices[0].Message.ToolCalls))
	}
	tc0 := resp.Choices[0].Message.ToolCalls[0]
	tc1 := resp.Choices[0].Message.ToolCalls[1]
	if tc0.ID != "call_a" || tc0.Type != "function" || tc0.Function.Name != "toolA" || tc0.Function.Arguments != `{"a":1}` {
		t.Errorf("tc0 mismatch: %+v", tc0)
	}
	if tc1.ID != "call_b" || tc1.Type != "function" || tc1.Function.Name != "toolB" || tc1.Function.Arguments != `{"b":2}` {
		t.Errorf("tc1 mismatch: %+v", tc1)
	}
}

func TestChatCompletionStream_EmptyTypeSynthesized(t *testing.T) {
	// Provider streams a tool_call with NO type field at all. Post-stream
	// normalization should default Type to "function".
	chunks := []string{
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","function":{"name":"noType","arguments":"{}"}}]}}]}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSEStream(w, chunks)
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	resp, err := c.ChatCompletionStream(t.Context(), &ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("ChatCompletionStream error: %v", err)
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.Type != "function" {
		t.Errorf("expected Type=function (synthesized post-stream), got %q", tc.Type)
	}
}

// --- NormalizeMessages tests (Fix 2) ---

func TestNormalizeMessages(t *testing.T) {
	t.Run("empty_tool_call_type_repaired", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "", Function: FunctionCall{Name: "foo", Arguments: "{}"}}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
		}
		out := NormalizeMessages(msgs)
		if len(out) != 3 {
			t.Fatalf("expected 3 messages (no drops), got %d", len(out))
		}
		if out[1].ToolCalls[0].Type != "function" {
			t.Errorf("expected Type=function, got %q", out[1].ToolCalls[0].Type)
		}
	})

	t.Run("orphaned_tool_message_dropped", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_known", Type: "function", Function: FunctionCall{Name: "foo"}}}},
			{Role: "tool", ToolCallID: "call_orphan", Content: "orphaned output"},
			{Role: "tool", ToolCallID: "call_known", Content: "ok"},
		}
		out := NormalizeMessages(msgs)
		// Expect: user, assistant, tool(call_known) — orphan dropped.
		if len(out) != 3 {
			t.Fatalf("expected 3 messages after orphan drop, got %d", len(out))
		}
		if out[2].ToolCallID != "call_known" {
			t.Errorf("expected surviving tool to be call_known, got %q", out[2].ToolCallID)
		}
	})

	t.Run("empty_assistant_content_coerced_to_nil_for_toolcall_only", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "f"}}}},
		}
		out := NormalizeMessages(msgs)
		if out[0].Content != nil {
			t.Errorf("expected Content=nil, got %v", out[0].Content)
		}
	})

	t.Run("empty_assistant_role_defaulted", func(t *testing.T) {
		msgs := []Message{
			{Role: "", Content: "hi", ToolCalls: nil},
		}
		out := NormalizeMessages(msgs)
		if out[0].Role != "assistant" {
			t.Errorf("expected Role=assistant, got %q", out[0].Role)
		}
	})

	t.Run("duplicate_consecutive_assistant_toolcall_ids_deduped", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "dup", Type: "function", Function: FunctionCall{Name: "f"}}}},
			// Replay: same ID, consecutive.
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "dup", Type: "function", Function: FunctionCall{Name: "f"}}}},
		}
		out := NormalizeMessages(msgs)
		if len(out) != 2 {
			t.Fatalf("expected 2 messages after dedupe, got %d: %+v", len(out), out)
		}
	})

	t.Run("empty_tool_call_id_synthesized", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "", Type: "function", Function: FunctionCall{Name: "f"}}}},
		}
		out := NormalizeMessages(msgs)
		if out[0].ToolCalls[0].ID == "" {
			t.Errorf("expected synthesized ID, got empty")
		}
		if !strings.HasPrefix(out[0].ToolCalls[0].ID, "call_") {
			t.Errorf("expected synthesized ID prefix 'call_', got %q", out[0].ToolCalls[0].ID)
		}
	})

	t.Run("clean_messages_unchanged_idempotent", func(t *testing.T) {
		msgs := []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "f", Arguments: "{}"}}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
			{Role: "assistant", Content: "final answer"},
		}
		out := NormalizeMessages(msgs)
		if len(out) != len(msgs) {
			t.Fatalf("expected %d messages (no drops), got %d", len(msgs), len(out))
		}
		// Run again to prove idempotency.
		out2 := NormalizeMessages(out)
		if len(out2) != len(out) {
			t.Fatalf("idempotency broken: %d -> %d", len(out), len(out2))
		}
	})

	t.Run("empty_slice_unchanged", func(t *testing.T) {
		out := NormalizeMessages(nil)
		if len(out) != 0 {
			t.Errorf("expected empty slice, got %d", len(out))
		}
	})

	t.Run("non_consecutive_duplicate_ids_not_deduped", func(t *testing.T) {
		// Two assistant messages declaring the same tool_call ID, separated
		// by a tool result, are NOT duplicates — they're a re-invocation.
		msgs := []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "f"}}}},
			{Role: "tool", ToolCallID: "c1", Content: "r1"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "f"}}}},
			{Role: "tool", ToolCallID: "c1", Content: "r2"},
		}
		out := NormalizeMessages(msgs)
		if len(out) != 4 {
			t.Fatalf("expected 4 messages (no dedupe across tool boundary), got %d", len(out))
		}
	})
}

// Integration: prove the streaming fix produces output that NormalizeMessages
// treats as a no-op on a healthy stream. Demonstrates Fix 1 + Fix 2 cooperate.
func TestNormalizeMessages_NoOpOnHealthyStreamOutput(t *testing.T) {
	chunks := []string{
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_healthy","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSEStream(w, chunks)
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	resp, err := c.ChatCompletionStream(t.Context(), &ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("ChatCompletionStream error: %v", err)
	}

	msgs := []Message{
		{Role: "user", Content: "hi"},
		resp.Choices[0].Message,
		{Role: "tool", ToolCallID: "call_healthy", Content: "result"},
	}
	out := NormalizeMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages (no-op), got %d", len(out))
	}
	if out[1].ToolCalls[0].Type != "function" || out[1].ToolCalls[0].ID != "call_healthy" {
		t.Errorf("healthy tool_call mutated by NormalizeMessages: %+v", out[1].ToolCalls[0])
	}
}

// Ensure imports are exercised.
var _ = time.Second
var _ = json.RawMessage(nil)
