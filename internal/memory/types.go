package memory

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Episode struct {
	Name              string `json:"name"`
	Content           string `json:"content"`
	SourceDescription string `json:"source_description"`
}

type AddMessagesRequest struct {
	GroupID  string    `json:"group_id"`
	Messages []Message `json:"messages"`
}

type SearchQuery struct {
	GroupIDs []string `json:"group_ids"`
	Query    string   `json:"query"`
	MaxFacts int      `json:"max_facts"`
}

type Fact struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Fact    string `json:"fact"`
	ValidAt string `json:"valid_at,omitempty"`
}

type SearchResults struct {
	Facts []Fact `json:"facts"`
}
