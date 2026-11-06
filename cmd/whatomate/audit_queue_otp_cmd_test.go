package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditStream_ClassifiesWithoutTouchingTheStream(t *testing.T) {
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx := context.Background()
	rdb.Del(ctx, queue.StreamName)
	defer rdb.Del(ctx, queue.StreamName)

	authID, plainID := uuid.New(), uuid.New()
	add := func(campaign uuid.UUID, params models.JSONB) string {
		job := queue.RecipientJob{CampaignID: campaign, RecipientID: uuid.New(), TemplateParams: params}
		b, _ := json.Marshal(job)
		id, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: queue.StreamName, Values: map[string]any{"type": "recipient", "payload": string(b)}}).Result()
		require.NoError(t, err)
		return id
	}
	done := add(authID, models.JSONB{"1": "111111"})        // delivered + acked earlier
	pend := add(authID, models.JSONB{"1": "222222"})        // delivered, still in the PEL
	undelivered := add(authID, models.JSONB{"1": "333333"}) // not read yet
	add(authID, models.JSONB{templateutil.SealedKey: "enc:xx", templateutil.SealedAtKey: 1})
	add(plainID, models.JSONB{"1": "John"})
	add(authID, nil)

	isAuth := func(id uuid.UUID) bool { return id == authID }
	// done is behind last-delivered and not pending; pend is behind it but pending.
	rep, err := auditStream(ctx, rdb, pend, map[string]bool{pend: true}, isAuth)
	require.NoError(t, err)

	assert.Equal(t, 6, rep.Total)
	assert.Equal(t, []string{done}, rep.DoneIDs, "only the processed entry is offered for removal")
	assert.Equal(t, 2, rep.ClearPending, "pending and undelivered clear codes are kept for the worker")
	assert.Equal(t, 1, rep.Sealed)
	assert.Equal(t, 1, rep.OtherParams)
	assert.Equal(t, 1, rep.NoParams)
	_ = undelivered

	n, _ := rdb.XLen(ctx, queue.StreamName).Result()
	assert.Equal(t, int64(6), n, "the audit never deletes")
}
