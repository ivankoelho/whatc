package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
)

// runAuditQueueOTP handles `whatomate audit-queue-otp`: a READ-ONLY look at the
// campaign stream for jobs that carry a one-time code in clear text. It never
// deletes anything; for entries that are safe to remove it prints the XDEL
// command so a person can run it after reviewing.
func runAuditQueueOTP(args []string) {
	fs := flag.NewFlagSet("audit-queue-otp", flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config file")
	_ = fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("failed to load config: %v", err)
	}
	db, err := database.NewPostgres(&cfg.Database, false)
	if err != nil {
		fatalf("failed to connect to the database: %v", err)
	}
	rdb, err := database.NewRedis(&cfg.Redis)
	if err != nil {
		fatalf("failed to connect to Redis: %v", err)
	}
	ctx := context.Background()

	// ids <= lastDelivered were handed to a worker; the PEL holds the ones
	// delivered but not yet acknowledged.
	lastDelivered := "0-0"
	groups, err := rdb.XInfoGroups(ctx, queue.StreamName).Result()
	if err != nil {
		if strings.Contains(err.Error(), "no such key") {
			fmt.Printf("stream %s does not exist: nothing to audit\n", queue.StreamName)
			return
		}
		fatalf("XINFO GROUPS: %v", err)
	}
	for _, g := range groups {
		fmt.Printf("group %s: consumers=%d pending=%d last-delivered=%s\n", g.Name, g.Consumers, g.Pending, g.LastDeliveredID)
		if g.Name == queue.ConsumerGroup {
			lastDelivered = g.LastDeliveredID
		}
	}
	if len(groups) > 1 {
		fmt.Println("WARNING: other consumer groups exist; XDEL would also remove entries they have not read")
	}
	pending := map[string]bool{}
	pel, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: queue.StreamName, Group: queue.ConsumerGroup, Start: "-", End: "+", Count: 100000}).Result()
	if err != nil && !strings.Contains(err.Error(), "NOGROUP") {
		fatalf("XPENDING: %v", err)
	}
	for _, p := range pel {
		pending[p.ID] = true
	}

	authCampaign := map[uuid.UUID]bool{}
	isAuth := func(id uuid.UUID) bool {
		if v, ok := authCampaign[id]; ok {
			return v
		}
		var category string
		db.Unscoped().Raw(`SELECT t.category FROM bulk_message_campaigns c JOIN templates t ON t.id = c.template_id WHERE c.id = ?`, id).Scan(&category)
		authCampaign[id] = templateutil.IsAuthCategory(category)
		return authCampaign[id]
	}

	rep, err := auditStream(ctx, rdb, lastDelivered, pending, isAuth)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("stream %s: %d entries | sealed=%d without-params=%d non-auth-params=%d | CLEAR OTP: pending/undelivered=%d, already processed=%d\n",
		queue.StreamName, rep.Total, rep.Sealed, rep.NoParams, rep.OtherParams, rep.ClearPending, rep.ClearDone)
	fmt.Println("(read-only; nothing was changed)")
	if len(rep.DoneIDs) > 0 {
		fmt.Println("Processed entries with a clear code. After reviewing, remove with:")
		for i := 0; i < len(rep.DoneIDs); i += 50 {
			fmt.Printf("  redis-cli XDEL %s %s\n", queue.StreamName, strings.Join(rep.DoneIDs[i:min(i+50, len(rep.DoneIDs))], " "))
		}
	}
}

// streamReport is what auditStream found.
type streamReport struct {
	Total, Sealed, NoParams, OtherParams int
	ClearPending, ClearDone              int
	DoneIDs                              []string
}

// auditStream classifies every entry of the campaign stream. It only reads.
func auditStream(ctx context.Context, rdb *redis.Client, lastDelivered string, pending map[string]bool, isAuth func(uuid.UUID) bool) (*streamReport, error) {
	rep := &streamReport{}
	start := "-"
	for {
		msgs, err := rdb.XRangeN(ctx, queue.StreamName, start, "+", 500).Result()
		if err != nil {
			return nil, fmt.Errorf("XRANGE: %w", err)
		}
		for _, m := range msgs {
			rep.Total++
			var job queue.RecipientJob
			payload, _ := m.Values["payload"].(string)
			if json.Unmarshal([]byte(payload), &job) != nil || len(job.TemplateParams) == 0 {
				rep.NoParams++
				continue
			}
			switch {
			case templateutil.IsSealed(job.TemplateParams):
				rep.Sealed++
			case !isAuth(job.CampaignID):
				rep.OtherParams++
			case pending[m.ID] || streamIDAfter(m.ID, lastDelivered):
				rep.ClearPending++ // still needed: the (new) worker sends it and deletes the entry afterwards
			default:
				rep.ClearDone++
				rep.DoneIDs = append(rep.DoneIDs, m.ID)
			}
		}
		if len(msgs) < 500 {
			return rep, nil
		}
		start = "(" + msgs[len(msgs)-1].ID
	}
}

// streamIDAfter reports whether stream id a is greater than b ("ms-seq").
func streamIDAfter(a, b string) bool {
	var am, as, bm, bs uint64
	_, _ = fmt.Sscanf(a, "%d-%d", &am, &as)
	_, _ = fmt.Sscanf(b, "%d-%d", &bm, &bs)
	return am > bm || (am == bm && as > bs)
}
