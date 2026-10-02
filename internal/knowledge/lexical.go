package knowledge

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// LexicalRetriever is the PostgreSQL full-text Retriever (config 'portuguese').
// The scope filter lives in the same WHERE as the text match, so it is applied
// before ranking and LIMIT: a better-ranked chunk the asker may not see can
// never take a slot.
type LexicalRetriever struct{ DB *gorm.DB }

type lexicalRow struct {
	ChunkID    uuid.UUID
	DocumentID uuid.UUID
	Title      string
	Heading    string
	Content    string
	SourceType string
	Origin     string
	Score      float64
}

const lexicalSQL = `
SELECT c.id AS chunk_id, c.document_id, d.title, c.heading, c.content,
       d.source_type, d.origin, ts_rank_cd(c.search_vector, q.query) AS score
FROM knowledge_chunks c
JOIN knowledge_documents d ON d.id = c.document_id AND d.deleted_at IS NULL
CROSS JOIN (SELECT websearch_to_tsquery('portuguese', ?) AS query) q
WHERE c.organization_id = ?
  AND c.status = 'active'
  AND (c.unit_id IS NULL OR c.unit_id = ?)
  AND (c.department_id IS NULL OR c.department_id = ?)
  AND c.search_vector @@ q.query
ORDER BY score DESC, c.document_id, c.chunk_index
LIMIT ?`

func (l LexicalRetriever) Retrieve(ctx context.Context, q Query) ([]Hit, error) {
	text := FoldForSearch(q.Text)
	if text == "" {
		return nil, nil
	}
	var rows []lexicalRow
	// A nil *uuid.UUID binds as NULL, and "unit_id = NULL" is never true: no
	// unit/department in the context means only global content matches.
	err := l.DB.WithContext(ctx).Raw(lexicalSQL, text, q.OrgID, q.UnitID, q.DepartmentID, q.Limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(rows))
	for _, r := range rows {
		anchor := ""
		if i := strings.LastIndex(r.Origin, "#"); i >= 0 {
			anchor = r.Origin[i+1:]
		}
		hits = append(hits, Hit{
			DocumentID: r.DocumentID, ChunkID: r.ChunkID, Title: r.Title, Heading: r.Heading,
			Content: r.Content, Score: r.Score,
			Citation: Citation{Title: r.Title, Heading: r.Heading, SourceType: r.SourceType, Origin: r.Origin, Anchor: anchor},
		})
	}
	return hits, nil
}

var _ Retriever = LexicalRetriever{}

// SchemaSQL is the index/column DDL AutoMigrate cannot express: the generated
// tsvector (built from the accent-folded copies, weights A=heading B=text), its
// GIN index, the scope index and the per-organization unique origin.
// Idempotent; run with the other index SQL.
var SchemaSQL = []string{
	`ALTER TABLE knowledge_chunks ADD COLUMN IF NOT EXISTS search_vector tsvector
	   GENERATED ALWAYS AS (
	     setweight(to_tsvector('portuguese', coalesce(search_heading, '')), 'A') ||
	     setweight(to_tsvector('portuguese', coalesce(search_text, '')), 'B')
	   ) STORED`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_search ON knowledge_chunks USING GIN (search_vector)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_scope ON knowledge_chunks(organization_id, status, unit_id, department_id)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_docs_org_origin ON knowledge_documents(organization_id, origin) WHERE origin <> '' AND deleted_at IS NULL`,
}

// VisibilityFor derives the visibility from the scope.
func VisibilityFor(unit, dept *uuid.UUID) string {
	switch {
	case dept != nil:
		return models.KnowledgeVisibilityDepartment
	case unit != nil:
		return models.KnowledgeVisibilityUnit
	}
	return models.KnowledgeVisibilityOrganization
}
