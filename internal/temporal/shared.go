package temporal

import (
	"github.com/angoo/agentfoundry-worker/internal/config"
	"github.com/angoo/agentfoundry-worker/internal/llm"
	"github.com/angoo/agentfoundry-worker/internal/memory"
)

const (
	TaskQueue    = "agentfoundry-worker"
	WorkflowType = "RunAgentWorkflow"
)

type LLMConfigInput struct {
	SchemaValidation bool `json:"schema_validation"`
}

type RunAgentParams struct {
	AgentID        string                   `json:"agent_id"`
	AgentName      string                   `json:"agent_name"`
	Message        string                   `json:"message"`
	History        []llm.Message            `json:"history,omitempty"`
	ResponseSchema *config.StructuredOutput `json:"response_schema,omitempty"`
	StreamID       string                   `json:"stream_id,omitempty"`
	LLMConfig      *LLMConfigInput          `json:"llm_config,omitempty"`
	MemoryEnabled       bool                     `json:"memory_enabled,omitempty"`
	MemorySearchAgentID string                   `json:"memory_search_agent_id,omitempty"`
	MemoryIngestAgentID string                   `json:"memory_ingest_agent_id,omitempty"`
	UserSubject         string                   `json:"user_subject,omitempty"`
}

type RunAgentResult struct {
	Response string        `json:"response"`
	History  []llm.Message `json:"history,omitempty"`
}

type ResolveAgentInput struct {
	AgentID string `json:"agent_id"`
}

type ResolveAgentResult struct {
	Definition *config.Definition `json:"definition"`
}

type LLMChatInput struct {
	Request   *llm.ChatRequest `json:"request"`
	StreamID  string           `json:"stream_id,omitempty"`
	LLMConfig *LLMConfigInput  `json:"llm_config,omitempty"`
	AgentID   string           `json:"agent_id"`
}

type LLMChatResult struct {
	Response *llm.ChatResponse `json:"response"`
}

type CallToolInput struct {
	ServerName string         `json:"server_name"`
	ToolName   string         `json:"tool_name"`
	Arguments  map[string]any `json:"arguments"`
	StreamID   string         `json:"stream_id,omitempty"`
}

type CallToolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

type ToolKind string

const (
	ToolKindMCP   ToolKind = "mcp"
	ToolKindAgent ToolKind = "agent"
)

type ToolOverride struct {
	Param string `json:"param"`
	Value string `json:"value"`
	Force bool   `json:"force"`
}

type ToolRoute struct {
	LLMName    string         `json:"llm_name"`
	Kind       ToolKind       `json:"kind"`
	ServerName string         `json:"server_name,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	AgentID    string         `json:"agent_id,omitempty"`
	AgentName  string         `json:"agent_name,omitempty"`
	Overrides  []ToolOverride `json:"overrides,omitempty"`
}

type SearchMemoryInput struct {
	Queries []string `json:"queries"`
	GroupID string   `json:"group_id"`
}

type SearchMemoryResult struct {
	Facts []string `json:"facts"`
}

type IngestEpisodeInput struct {
	Episodes []memory.Episode `json:"episodes"`
	GroupID  string           `json:"group_id"`
}

type MemorySearchAgentOutput struct {
	Queries []string `json:"queries"`
}

type MemoryIngestAgentOutput struct {
	Episodes []memory.Episode `json:"episodes"`
}
