package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) Search(ctx context.Context, groupID, query string, maxFacts int) ([]Fact, error) {
	req := SearchQuery{
		GroupIDs: []string{groupID},
		Query:    query,
		MaxFacts: maxFacts,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	var result SearchResults
	if err := c.do(httpReq, &result); err != nil {
		return nil, fmt.Errorf("search graphiti: %w", err)
	}
	return result.Facts, nil
}

func (c *Client) IngestMessages(ctx context.Context, groupID string, messages []Message) error {
	req := AddMessagesRequest{
		GroupID:  groupID,
		Messages: messages,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal ingest request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create ingest request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if err := c.do(httpReq, nil); err != nil {
		return fmt.Errorf("ingest graphiti: %w", err)
	}
	return nil
}

func (c *Client) IngestEpisodes(ctx context.Context, groupID string, episodes []Episode) error {
	messages := make([]Message, len(episodes))
	now := time.Now().UTC().Format(time.RFC3339)
	for i, ep := range episodes {
		messages[i] = Message{
			Role:              "user",
			RoleType:          "user",
			Content:           ep.Content,
			Name:              ep.Name,
			SourceDescription: ep.SourceDescription,
			Timestamp:         now,
		}
	}
	return c.IngestMessages(ctx, groupID, messages)
}

func (c *Client) do(req *http.Request, result any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Graphiti's ingest endpoint (/messages) returns 202 Accepted when messages
	// are queued for async processing. Search returns 200. Accept any 2xx as
	// success and only decode the body into a typed result on 200.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody bytes.Buffer
		errBody.ReadFrom(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, errBody.String())
	}

	if result != nil && resp.StatusCode == http.StatusOK {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}
