package database

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/stretchr/testify/assert"
)

// The server migration reads getIndexes(), tests read CreateIndexes(): both must
// carry the knowledge DDL (a missed one only shows up when the real app starts).
func TestGetIndexes_IncludesTheKnowledgeSchema(t *testing.T) {
	all := getIndexes()
	for _, stmt := range knowledge.SchemaSQL {
		assert.Contains(t, all, stmt)
	}
}
