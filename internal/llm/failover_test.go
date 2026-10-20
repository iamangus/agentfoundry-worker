package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSingleAttemptDoesNotRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer server.Close()
	client := NewClient(ClientConfig{BaseURL: server.URL, SingleAttempt: true})
	_, err := client.ChatCompletion(context.Background(), &ChatRequest{Model: "test"})
	if calls != 1 || !Retryable(err) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRetryableErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&HTTPError{Status: 429}, true}, {&HTTPError{Status: 500}, true}, {&HTTPError{Status: 501}, true},
		{&HTTPError{Status: 400}, false}, {&HTTPError{Status: 401}, false}, {&HTTPError{Status: 403}, false},
		{context.Canceled, false}, {context.DeadlineExceeded, true}, {io.ErrUnexpectedEOF, true}, {errors.New("invalid JSON"), false},
	} {
		if got := Retryable(tc.err); got != tc.want {
			t.Errorf("Retryable(%v)=%v", tc.err, got)
		}
	}
}

func TestStreamRejectsTruncationAndProviderErrors(t *testing.T) {
	for _, body := range []string{"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", "data: {\"error\":{\"message\":\"overloaded\"}}\n\n"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, body)
		}))
		_, err := NewClient(ClientConfig{BaseURL: server.URL}).ChatCompletionStream(context.Background(), &ChatRequest{}, nil)
		server.Close()
		if err == nil || !Retryable(err) {
			t.Fatalf("truncated/error stream accepted: %v", err)
		}
	}
}
