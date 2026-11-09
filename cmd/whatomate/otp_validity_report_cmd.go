package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/templateutil"
)

// runOTPValidityReport handles `whatomate otp-validity-report`: a read-only
// impact report for turning on otp.require_valid_until. It writes nothing.
func runOTPValidityReport(args []string) {
	fs := flag.NewFlagSet("otp-validity-report", flag.ExitOnError)
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
	policy := templateutil.OTPPolicy{MaxValidity: cfg.OTP.MaxValidity(), RequireValidUntil: cfg.OTP.RequireValidUntil}
	impacts, err := authmask.ValidityReport(db, time.Now(), policy)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("send window: %s | require_valid_until currently: %v\n", policy.MaxValidity, policy.RequireValidUntil)
	var noValidity int
	for _, im := range impacts {
		noValidity += im.NoValidity
		sched := "-"
		if im.ScheduledAt != nil {
			sched = im.ScheduledAt.UTC().Format(time.RFC3339)
		}
		fmt.Printf("campaign %s %q status=%s scheduled=%s | with validity: %d, expired: %d, WITHOUT validity (would fail if enforced): %d (clear text: %d)\n",
			im.CampaignID, im.Name, im.Status, sched, im.WithValidity, im.Expired, im.NoValidity, im.Plaintext)
	}
	fmt.Printf("%d campaign(s), %d actionable recipient(s) would be refused with otp_validity_missing if enforcement were on now (read-only; nothing changed)\n", len(impacts), noValidity)
}
