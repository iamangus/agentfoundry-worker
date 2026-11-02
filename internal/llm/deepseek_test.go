package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekReasoningSurvivesToolTurns(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if calls == 2 {
					if req.Messages[0].ReasoningContent != "Need lookup" {
						t.Errorf("reasoning lost: %+v", req.Messages)
					}
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
					return
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"Need \"}}]}\n\n")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"lookup\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","reasoning_content":"Need lookup","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`)
				}
			}))
			defer server.Close()
			client := NewClient(ClientConfig{BaseURL: server.URL, DefaultModel: "discovered-model", SingleAttempt: true})
			req := &ChatRequest{Messages: []Message{{Role: "user", Content: "question"}}}
			var response *ChatResponse
			var err error
			if streaming {
				response, err = client.ChatCompletionStream(context.Background(), req, nil)
			} else {
				response, err = client.ChatCompletion(context.Background(), req)
			}
			if err != nil {
				t.Fatal(err)
			}
			message := response.Choices[0].Message
			if message.ReasoningContent != "Need lookup" || len(message.ToolCalls) != 1 {
				t.Fatalf("lost reasoning or tool call: %+v", message)
			}
			_, err = client.ChatCompletion(context.Background(), &ChatRequest{Messages: []Message{message, {Role: "tool", ToolCallID: "call-1", Content: "result"}}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
