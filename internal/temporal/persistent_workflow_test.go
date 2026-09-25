package temporal

import (
	"strings"
	"testing"

	"github.com/angoo/agentfoundry-worker/internal/llm"
)

func TestTrimPersistentHistoryKeepsRecentCompleteTurns(t *testing.T) {
	var history []llm.Message
	for i := 0; i < 100; i++ {
		history = append(history,
			llm.Message{Role: "user", Content: "question"},
			llm.Message{Role: "assistant", Content: "answer"},
		)
	}
	trimmed := trimPersistentHistory(history)
	if len(trimmed) > persistentHistoryMessages || trimmed[0].Role != "user" || trimmed[len(trimmed)-1].Role != "assistant" {
		t.Fatalf("trimmed %d messages with endpoints %q, %q", len(trimmed), trimmed[0].Role, trimmed[len(trimmed)-1].Role)
	}
	if len(history) != 200 {
		t.Fatal("trim mutated the input")
	}
}

func TestTrimPersistentHistoryHonorsPayloadBudget(t *testing.T) {
	var history []llm.Message
	for i := 0; i < 10; i++ {
		history = append(history, llm.Message{Role: "user", Content: strings.Repeat("a", 50*1024)})
		history = append(history, llm.Message{Role: "assistant", Content: "ok"})
	}
	trimmed := trimPersistentHistory(history)
	if len(trimmed) >= len(history) || len(trimmed) == 0 || trimmed[0].Role != "user" {
		t.Fatalf("unexpected trimmed history: %d messages", len(trimmed))
	}
}
