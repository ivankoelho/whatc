package knowledge

import (
	"context"
	"sort"
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
	if q.Strategy == Relaxed {
		return l.retrieveRelaxed(ctx, q)
	}
	text := SearchForm(q.Text)
	if text == "" {
		return []Hit{}, nil // an empty list, never null in the JSON
	}
	return l.run(ctx, text, q)
}

// retrieveRelaxed is the chatbot's strategy: the text is turned into terms (never syntax) and
// searched by relaxedRetrieve. The scope filter is in the SQL of both passes.
func (l LexicalRetriever) retrieveRelaxed(ctx context.Context, q Query) ([]Hit, error) {
	terms := PlainTerms(q.Text)
	return relaxedRetrieve(terms, q.Limit, func(tsText string) ([]Hit, error) {
		return l.run(ctx, tsText, q)
	})
}

// relaxedRetrieve runs the strict pass (every term, AND) and, only when it gives fewer than
// MinHits, the OR pass. The strict results are the main evidence: they ALWAYS come first, in the
// order of the strict query. The OR pass only COMPLETES them: chunks not already present, among
// themselves in the order of the OR query. The scores of two different queries are never
// compared with each other (ts_rank_cd of an AND query and of an OR query are not on one scale).
// The limit is applied after the composition.
func relaxedRetrieve(terms []string, limit int, run func(tsText string) ([]Hit, error)) ([]Hit, error) {
	if len(terms) == 0 {
		return []Hit{}, nil
	}
	hits, err := run(strings.Join(terms, " "))
	if err != nil || len(hits) >= MinHits || len(terms) == 1 {
		return hits, err
	}
	more, err := run(strings.Join(terms, " or "))
	if err != nil {
		return nil, err
	}
	seen := make(map[uuid.UUID]bool, len(hits))
	for _, h := range hits {
		seen[h.ChunkID] = true
	}
	var extra []Hit
	for _, h := range more {
		if !seen[h.ChunkID] {
			extra = append(extra, h)
		}
	}
	sort.SliceStable(extra, func(i, j int) bool { return extra[i].Score > extra[j].Score }) // same query: comparable
	hits = append(hits, extra...)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
// run executes one full-text query (tsText is a websearch_to_tsquery input) with the scope filter.
func (l LexicalRetriever) run(ctx context.Context, text string, q Query) ([]Hit, error) {
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
	// archived_by is a closed list: '' (not archived by anyone), 'user' or 'import'.
	`DO $$ BEGIN
		ALTER TABLE knowledge_documents ADD CONSTRAINT chk_knowledge_archived_by CHECK (archived_by IN ('', 'user', 'import'));
	EXCEPTION WHEN duplicate_object THEN NULL;
	END $$`,
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
