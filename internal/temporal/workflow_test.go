package temporal

import (
	"encoding/json"
	"testing"

	"github.com/angoo/agentfoundry-worker/internal/orchestrator"
)

func TestBuildToolMessageContentImage(t *testing.T) {
	blocks := []orchestrator.ContentBlock{
		{Type: "image", Data: "aGVsbG8=", MIMEType: "image/png"},
	}

	content := buildToolMessageContent("", blocks)

	parts, ok := content.([]any)
	if !ok {
		t.Fatalf("expected []any content, got %T", content)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}

	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map part, got %T", parts[0])
	}
	if part["type"] != "image_url" {
		t.Fatalf("expected image_url part, got %+v", part)
	}
	imageURL, ok := part["image_url"].(map[string]string)
	if !ok {
		t.Fatalf("expected image_url map, got %T", part["image_url"])
	}
	expected := "data:image/png;base64,aGVsbG8="
	if imageURL["url"] != expected {
		t.Fatalf("expected url %q, got %q", expected, imageURL["url"])
	}
}

func TestBuildToolMessageContentMixed(t *testing.T) {
	blocks := []orchestrator.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "image", Data: "aGVsbG8=", MIMEType: "image/jpeg"},
	}

	content := buildToolMessageContent("", blocks)
	parts, ok := content.([]any)
	if !ok {
		t.Fatalf("expected []any content, got %T", content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	first, _ := parts[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "hello" {
		t.Fatalf("unexpected first part: %+v", parts[0])
	}
	second, _ := parts[1].(map[string]any)
	imageURL, _ := second["image_url"].(map[string]string)
	if imageURL["url"] != "data:image/jpeg;base64,aGVsbG8=" {
		t.Fatalf("unexpected second part: %+v", parts[1])
	}
}

func TestBuildToolMessageContentTextOnly(t *testing.T) {
	content := buildToolMessageContent("plain result", nil)
	if content != "plain result" {
		t.Fatalf("expected plain string passthrough, got %T %v", content, content)
	}
}

func TestBuildToolMessageContentEmptyMIME(t *testing.T) {
	blocks := []orchestrator.ContentBlock{
		{Type: "image", Data: "aGVsbG8="},
	}

	content := buildToolMessageContent("", blocks)
	parts, ok := content.([]any)
	if !ok {
		t.Fatalf("expected []any content, got %T", content)
	}
	part, _ := parts[0].(map[string]any)
	imageURL, _ := part["image_url"].(map[string]string)
	if imageURL["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected default png mime, got %q", imageURL["url"])
	}
}

func TestBuildToolMessageContentSerializesAsOpenAIFormat(t *testing.T) {
	blocks := []orchestrator.ContentBlock{
		{Type: "image", Data: "aGVsbG8=", MIMEType: "image/png"},
	}

	content := buildToolMessageContent("", blocks)
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0]["type"] != "image_url" {
		t.Fatalf("unexpected serialized content: %s", data)
	}
}
