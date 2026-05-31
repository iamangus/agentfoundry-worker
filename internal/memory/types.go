package memory

type Message struct {
	Role              string `json:"role"`
	RoleType          string `json:"role_type"`
	Content           string `json:"content"`
	Name              string `json:"name,omitempty"`
	SourceDescription string `json:"source_description,omitempty"`
	Timestamp         string `json:"timestamp"`
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
