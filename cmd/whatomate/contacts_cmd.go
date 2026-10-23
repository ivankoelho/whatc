package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/database"
)

const contactsUsage = `Usage:
  whatomate contacts merge-duplicates -org <organization-id> [-apply] [-config config.toml]`

// runContacts handles `whatomate contacts`. merge-duplicates is a dry run unless
// -apply is given: back up the database first, the merge re-points message
// history and soft-deletes the duplicate rows.
func runContacts(args []string) {
	if len(args) == 0 || args[0] != "merge-duplicates" {
		fmt.Println(contactsUsage)
		os.Exit(1)
	}
	fs := flag.NewFlagSet("contacts "+args[0], flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config file")
	orgFlag := fs.String("org", "", "Organization ID (required)")
	apply := fs.Bool("apply", false, "Write the merge (default: report only)")
	_ = fs.Parse(args[1:])

	orgID, err := uuid.Parse(*orgFlag)
	if err != nil {
		fatalf("-org must be a valid organization UUID")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("failed to load config: %v", err)
	}
	db, err := database.NewPostgres(&cfg.Database, false)
	if err != nil {
		fatalf("failed to connect to the database: %v", err)
	}
	rep, err := contactutil.MergeDuplicateContacts(db, orgID, *apply)
	if err != nil {
		fatalf("%v", err)
	}
	mode := "Dry run (nothing written)"
	if *apply {
		mode = "Applied"
	}
	fmt.Printf("%s: %d groups, %d duplicates merged, %d child rows moved, %d groups skipped\n",
		mode, rep.Groups, rep.Duplicates, rep.Moved, len(rep.Skipped))
	for _, s := range rep.Skipped {
		fmt.Println("  skipped:", s)
	}
}
