package knowledge

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedRun answers the strict and the OR query with fixed hits and records which ran.
type scriptedRun struct {
	strict, or []Hit
	calls      []string
	orErr      error
}

func (s *scriptedRun) run(text string) ([]Hit, error) {
	s.calls = append(s.calls, text)
	if len(s.calls) == 1 {
		return s.strict, nil
	}
	return s.or, s.orErr
}

func h(id uuid.UUID, title string, score float64) Hit {
	return Hit{ChunkID: id, DocumentID: id, Title: title, Score: score}
}

func titlesOf(hits []Hit) []string {
	out := []string{}
	for _, x := range hits {
		out = append(out, x.Title)
	}
	return out
}

var terms = []string{"prazo", "entrega", "argamassa"}

// The scenario of the post-merge validation, with the scores of the real run.
func TestRelaxedRetrieve_TheStrictHitStaysFirstEvenWithALowerScore(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	s := &scriptedRun{
		strict: []Hit{h(a, "A", 0.856)},                   // AND query: A matches every term
		or:     []Hit{h(b, "B", 1.2), h(a, "A", 4.2)},     // OR query: B (partial) outscores A's strict score
	}
	got, err := relaxedRetrieve(terms, 10, s.run)
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B"}, titlesOf(got), "never B, A")
	assert.Equal(t, 0.856, got[0].Score, "scores of different queries are not recomputed or mixed")
	assert.Equal(t, []string{"prazo entrega argamassa", "prazo or entrega or argamassa"}, s.calls)
}

func TestRelaxedRetrieve_OneStrictThenTheOrOnlyHitsInTheirOwnOrder(t *testing.T) {
	a1, b1, b2, b3 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	// one strict hit is below MinHits (2), so the OR pass completes it; its rows come unsorted here
	s := &scriptedRun{
		strict: []Hit{h(a1, "S1", 0.1)},
		or:     []Hit{h(b2, "O-low", 0.5), h(a1, "S1", 9), h(b1, "O-high", 3.0), h(b3, "O-mid", 1.5)},
	}
	got, err := relaxedRetrieve(terms, 10, s.run)
	require.NoError(t, err)
	assert.Equal(t, []string{"S1", "O-high", "O-mid", "O-low"}, titlesOf(got),
		"the strict hit first; the OR-only hits among themselves by their OR score; no duplicate of S1")
}

func TestRelaxedRetrieve_ZeroStrictIsOnlyOr_InTheOrderOfTheOrQuery(t *testing.T) {
	x, y := uuid.New(), uuid.New()
	s := &scriptedRun{strict: nil, or: []Hit{h(x, "X", 1), h(y, "Y", 2)}}
	got, err := relaxedRetrieve(terms, 10, s.run)
	require.NoError(t, err)
	assert.Equal(t, []string{"Y", "X"}, titlesOf(got))
	assert.Len(t, s.calls, 2)
}

func TestRelaxedRetrieve_MinHitsIsPreserved_TwoStrictMeansNoOrQuery(t *testing.T) {
	assert.Equal(t, 2, MinHits)
	a, b := uuid.New(), uuid.New()
	s := &scriptedRun{strict: []Hit{h(a, "S1", 0.3), h(b, "S2", 0.2)}, or: []Hit{h(uuid.New(), "never", 99)}}
	got, err := relaxedRetrieve(terms, 10, s.run)
	require.NoError(t, err)
	assert.Equal(t, []string{"S1", "S2"}, titlesOf(got), "the strict results are used as they are")
	assert.Len(t, s.calls, 1, "no OR query")

	// exactly one strict hit is below MinHits: the OR pass runs
	s = &scriptedRun{strict: []Hit{h(a, "S1", 0.3)}, or: []Hit{h(b, "O1", 5)}}
	got, err = relaxedRetrieve(terms, 10, s.run)
	require.NoError(t, err)
	assert.Equal(t, []string{"S1", "O1"}, titlesOf(got))
	assert.Len(t, s.calls, 2)
}

func TestRelaxedRetrieve_TheLimitIsAppliedAfterTheComposition(t *testing.T) {
	a := uuid.New()
	s := &scriptedRun{
		strict: []Hit{h(a, "S", 0.1)},
		or:     []Hit{h(uuid.New(), "O1", 9), h(uuid.New(), "O2", 8), h(uuid.New(), "O3", 7)},
	}
	got, err := relaxedRetrieve(terms, 3, s.run)
	require.NoError(t, err)
	assert.Equal(t, []string{"S", "O1", "O2"}, titlesOf(got), "the strict hit keeps its place, the OR hits fill the rest")

	// a limit of 1 keeps only the strict evidence
	s = &scriptedRun{strict: []Hit{h(a, "S", 0.1)}, or: []Hit{h(uuid.New(), "O1", 9)}}
	got, err = relaxedRetrieve(terms, 1, s.run)
	require.NoError(t, err)
	assert.Equal(t, []string{"S"}, titlesOf(got))
}

func TestRelaxedRetrieve_OneTermIsOnlyTheStrictQuery_EmptyIsEmpty_ErrorsPass(t *testing.T) {
	s := &scriptedRun{strict: []Hit{h(uuid.New(), "S", 1)}}
	got, err := relaxedRetrieve([]string{"prazo"}, 10, s.run)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Len(t, s.calls, 1, "a single term: AND and OR are the same query")

	got, err = relaxedRetrieve(nil, 10, s.run)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)

	boom := errors.New("boom")
	_, err = relaxedRetrieve(terms, 10, (&scriptedRun{strict: nil, orErr: boom}).run)
	assert.ErrorIs(t, err, boom)
}
