package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamPreservesUsageAndCorrelation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			StreamOptions map[string]bool `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !body.StreamOptions["include_usage"] {
			t.Error("stream usage not requested")
		}
		if r.Header.Get("X-AgentFoundry-Activity-ID") != "activity" || r.Header.Get("X-AgentFoundry-HTTP-Attempt") != "1" {
			t.Error("missing correlation")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5,\"total_tokens\":105,\"prompt_tokens_details\":{\"cached_tokens\":80},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewClient(ClientConfig{BaseURL: server.URL, Headers: map[string]string{"X-AgentFoundry-Activity-ID": "activity"}})
	response, err := client.ChatCompletionStream(context.Background(), &ChatRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u := response.Usage
	if u.PromptTokens != 100 || u.CompletionTokens != 5 || u.PromptTokensDetails == nil || *u.PromptTokensDetails.CachedTokens != 80 || u.CompletionTokensDetails == nil || *u.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("lost usage: %+v", u)
	}
}
