package main

import (
	"flag"
	"fmt"

	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
)

// runMaskAuthCodes handles `whatomate mask-auth-codes`: masks one-time codes
// already stored in AUTHENTICATION message history and in campaign recipients.
// A dry run (the default) only counts; back up the database before -apply.
func runMaskAuthCodes(args []string) {
	fs := flag.NewFlagSet("mask-auth-codes", flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config file")
	apply := fs.Bool("apply", false, "Write the changes (default: report only)")
	sealPending := fs.Bool("seal-pending", false, "Also encrypt (not drop) codes of campaign recipients still waiting to be sent or retried; needs app.encryption_key")
	_ = fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("failed to load config: %v", err)
	}
	if *apply && *sealPending && cfg.App.EncryptionKey == "" {
		fatalf("-seal-pending needs app.encryption_key")
	}
	db, err := database.NewPostgres(&cfg.Database, false)
	if err != nil {
		fatalf("failed to connect to the database: %v", err)
	}
	mode := "Dry run (nothing written)"
	if *apply {
		mode = "Applied"
	}

	rep, err := authmask.Run(db, *apply)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("%s - messages: %d inspected, %d masked, %d already masked, %d need manual review\n",
		mode, rep.Candidates, rep.Masked, rep.AlreadyMasked, rep.Review)
	for _, id := range rep.ReviewIDs {
		fmt.Println("  review:", id)
	}

	rr, err := authmask.Recipients(db, cfg.App.EncryptionKey, *apply, *sealPending)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("%s - campaign recipients: %d clear codes dropped (sent/cancelled/deleted), %d still needed (pending/retryable; sealed only with -seal-pending), %d already sealed\n",
		mode, rr.Scrub, rr.SealPending, rr.AlreadySealed)
}
