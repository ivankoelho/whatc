package knowledge

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Query is a retrieval request. UnitID/DepartmentID are the CONTEXT of whoever
// asks (already authorized by the caller): documents of other units/departments
// are never candidates. Nil means "no unit/department": only global content.
type Query struct {
	OrgID        uuid.UUID
	Text         string
	UnitID       *uuid.UUID
	DepartmentID *uuid.UUID
	Limit        int
	// Strategy is Strict (the zero value: the API and the admin search tool) or Relaxed
	// (the chatbot, free customer text).
	Strategy Strategy
}

// Strategy says how Text is read and matched.
type Strategy int

const (
	// Strict reads Text as a websearch query (quotes, "-" and "or" are syntax); every term
	// must match. It is the ONLY strategy the HTTP API exposes.
	Strict Strategy = iota
	// Relaxed is for free text typed by a customer: it is NEVER read as syntax (a "-" or a
	// quote typed by a customer is just punctuation). The text becomes terms, tried as AND
	// first and, when fewer than MinHits come back, as OR. The scope filter applies to both.
	Relaxed
)

// MinHits is how many results the strict pass must give before the relaxed strategy
// gives up on the OR pass.
const MinHits = 2

// Citation is what a consumer needs to say where a passage came from.
type Citation struct {
	Title      string `json:"title"`
	Heading    string `json:"heading,omitempty"`
	SourceType string `json:"source_type"`
	Origin     string `json:"origin,omitempty"`
	Anchor     string `json:"anchor,omitempty"`
}

// Hit is one retrieved chunk. Score is a lexical relevance (ts_rank_cd): only
// comparable between the hits of the same response, not a probability.
type Hit struct {
	DocumentID uuid.UUID `json:"document_id"`
	ChunkID    uuid.UUID `json:"chunk_id"`
	Title      string    `json:"title"`
	Heading    string    `json:"heading,omitempty"`
	Content    string    `json:"content"`
	Score      float64   `json:"score"`
	Citation   Citation  `json:"citation"`
}

// Retriever is the contract the rest of the system depends on. The PostgreSQL
// full-text implementation is the first one; a vector or hybrid retriever can
// replace it without touching Service, the API or any future chatbot/assistant.
type Retriever interface {
	Retrieve(ctx context.Context, q Query) ([]Hit, error)
}

const (
	DefaultLimit = 5
	MaxLimit     = 20
	maxQueryLen  = 500
)

// ErrEmptyQuery is returned for a blank query.
var ErrEmptyQuery = errors.New("empty query")

// Service validates a query and delegates to a Retriever. It knows nothing
// about PostgreSQL, accents, or any AI provider.
type Service struct{ Retriever Retriever }

func (s Service) Search(ctx context.Context, q Query) ([]Hit, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return nil, ErrEmptyQuery
	}
	if r := []rune(q.Text); len(r) > maxQueryLen {
		q.Text = string(r[:maxQueryLen])
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
	return s.Retriever.Retrieve(ctx, q)
}
