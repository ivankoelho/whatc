package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/knowledge"
)

const knowledgeUsage = `Usage:
  whatomate knowledge import-manuals -org <organization-id> [-dir manuais] [-unit <id>] [-department <id>] [-dry-run] [-config config.toml]
  whatomate knowledge reindex -org <organization-id> [-all] [-config config.toml]`

// runKnowledge handles the `whatomate knowledge` subcommands. They are explicit,
// offline operations: nothing here is exposed as an HTTP endpoint for the import,
// and nothing runs during -migrate.
func runKnowledge(args []string) {
	if len(args) == 0 || (args[0] != "import-manuals" && args[0] != "reindex") {
		fmt.Println(knowledgeUsage)
		os.Exit(1)
	}
	fs := flag.NewFlagSet("knowledge "+args[0], flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config file")
	orgFlag := fs.String("org", "", "Organization ID (required)")
	dir := fs.String("dir", "manuais", "Directory with the manual .html files (import-manuals)")
	unitFlag := fs.String("unit", "", "Restrict NEW documents to this unit (import-manuals, optional)")
	deptFlag := fs.String("department", "", "Restrict NEW documents to this department (import-manuals, optional)")
	dryRun := fs.Bool("dry-run", false, "Report what would change, write nothing (import-manuals)")
	all := fs.Bool("all", false, "Reindex every document, not only the stale ones (reindex)")
	_ = fs.Parse(args[1:])

	orgID, err := uuid.Parse(*orgFlag)
	if err != nil {
		fatalf("-org must be a valid organization UUID")
	}
	unit, err := optionalUUID(*unitFlag)
	if err != nil {
		fatalf("-unit must be a UUID")
	}
	dept, err := optionalUUID(*deptFlag)
	if err != nil {
		fatalf("-department must be a UUID")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("failed to load config: %v", err)
	}
	db, err := database.NewPostgres(&cfg.Database, false)
	if err != nil {
		fatalf("failed to connect to the database: %v", err)
	}
	// The organization, unit and department must exist (and belong together) before anything is written.
	if err := knowledge.ValidateImportTarget(db, orgID, unit, dept); err != nil {
		fatalf("%v", err)
	}

	if args[0] == "reindex" {
		res, err := knowledge.ReindexOrg(db, orgID, !*all)
		if err != nil {
			fatalf("%v", err)
		}
		fmt.Printf("Reindexed: %d documents, %d chunks, %d skipped, %d stale chunks remaining (index version %d)\n",
			res.Documents, res.Chunks, res.Skipped, res.StaleRemaining, knowledge.IndexVersion)
		return
	}

	res, err := knowledge.ImportManuals(db, orgID, unit, dept, *dir, *dryRun)
	prefix := "Manuals imported"
	if *dryRun {
		prefix = "Dry run (nothing written)"
	}
	fmt.Printf("%s: %d created, %d updated, %d unchanged, %d reactivated, %d archived\n",
		prefix, res.Created, res.Updated, res.Unchanged, res.Reactivated, res.Archived)
	for _, o := range res.ArchivedOrigins {
		fmt.Println("  archived (section left the HTML):", o)
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}

func optionalUUID(s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
