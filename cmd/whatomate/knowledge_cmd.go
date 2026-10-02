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

// runKnowledge handles `whatomate knowledge import-manuals`. The import is
// explicit and offline: it reads the HTML files once and stores text + chunks;
// nothing reads the files at query time. There is deliberately no HTTP endpoint
// for it in 8A.
func runKnowledge(args []string) {
	if len(args) == 0 || args[0] != "import-manuals" {
		fmt.Println("Usage: whatomate knowledge import-manuals -org <organization-id> [-dir manuais] [-unit <id>] [-department <id>] [-config config.toml]")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("knowledge import-manuals", flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config file")
	orgFlag := fs.String("org", "", "Organization ID (required)")
	dir := fs.String("dir", "manuais", "Directory with the manual .html files")
	unitFlag := fs.String("unit", "", "Restrict the documents to this unit (optional)")
	deptFlag := fs.String("department", "", "Restrict the documents to this department (optional)")
	_ = fs.Parse(args[1:])

	orgID, err := uuid.Parse(*orgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: -org must be a valid organization UUID")
		os.Exit(1)
	}
	unit, err := optionalUUID(*unitFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: -unit must be a UUID")
		os.Exit(1)
	}
	dept, err := optionalUUID(*deptFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: -department must be a UUID")
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to load config:", err)
		os.Exit(1)
	}
	db, err := database.NewPostgres(&cfg.Database, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to connect to the database:", err)
		os.Exit(1)
	}

	res, err := knowledge.ImportManuals(db, orgID, unit, dept, *dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("Manuals imported: %d created, %d updated, %d unchanged\n", res.Created, res.Updated, res.Unchanged)
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
