package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestAsyncPublisherCloseWhilePublishing reproduces the crash that brought the
// worker down: Close() fires while the publisher goroutine is mid-publish
// (publishShort can block for up to 5s), then the goroutine exits via <-p.done.
// The old code closed p.done from the goroutine too, panicking with
// "close of closed channel".
func TestAsyncPublisherCloseWhilePublishing(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})

	var (
		mu      sync.Mutex
		gotPath string
		gotBody map[string]string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		mu.Lock()
		gotPath = r.URL.Path
		_ = json.Unmarshal(b, &gotBody)
		mu.Unlock()

		close(started)
		<-release
		close(handlerDone)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := NewClient(Config{URL: srv.URL, APIKey: "test-key"})
	pub := NewAsyncPublisher(context.Background(), client, "stream-1")
	defer pub.Close()

	pub.PublishToken("hello")

	<-started // publisher goroutine is now blocked inside publishShort

	pub.Close() // the exact race: Close fires while the goroutine is mid-publish

	close(release)
	<-handlerDone

	// Give the goroutine a moment to finish its request and exit via <-p.done.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if gotPath != "/api/internal/streams/stream-1/tokens" {
		t.Errorf("path = %q, want .../stream-1/tokens", gotPath)
	}
	if gotBody["token"] != "hello" {
		t.Errorf("body = %v, want token=hello", gotBody)
	}
	mu.Unlock()

	// After Close the consumer goroutine must have exited. Prove it by
	// overflowing the 256-buffered channel: with no consumer, sends must drop.
	for i := 0; i < 300; i++ {
		pub.PublishToken("post-close")
	}
	if pub.dropped.Load() == 0 {
		t.Fatal("publishes after Close were not dropped: goroutine still consuming")
	}

	// Idempotency: a second Close must be a no-op, not a double-close panic.
	pub.Close()
}

// TestAsyncPublisherCloseIdempotent guards against double-close panics from
// Close() being called more than once, and publishes racing a Close.
func TestAsyncPublisherCloseIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := NewClient(Config{URL: srv.URL, APIKey: "test-key"})
	pub := NewAsyncPublisher(context.Background(), client, "s")

	pub.Close()
	pub.Close()

	pub.PublishToken("a")
	pub.PublishEvent("status", "done")
	time.Sleep(10 * time.Millisecond)
}

// TestAsyncPublisherContextCancel covers the goroutine exiting via ctx.Done
// racing a Close() — the other interleaving that could double-close.
func TestAsyncPublisherContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pub := NewAsyncPublisher(ctx, NewClient(Config{URL: "http://127.0.0.1:1", APIKey: "k"}), "s")

	cancel()
	pub.Close()
	time.Sleep(10 * time.Millisecond)
}
