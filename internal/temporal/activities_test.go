package temporal

import (
	"strings"
	"testing"
)

func TestLLMToolNameBoundsLongMCPNames(t *testing.T) {
	server := strings.Repeat("a", 120)
	name := llmToolName(server, "search_and_replace")
	if len(name) > maxLLMToolNameLength {
		t.Fatalf("tool name length = %d, want <= %d", len(name), maxLLMToolNameLength)
	}
	if name != llmToolName(server, "search_and_replace") {
		t.Fatal("long MCP tool name is not stable")
	}
	if name == llmToolName(server, "read_file") {
		t.Fatal("distinct MCP tools received the same alias")
	}
}

func TestLLMToolNamePreservesShortNames(t *testing.T) {
	if got, want := llmToolName("server", "tool"), "server__tool"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
}
