package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateaudit"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/zerodha/logf"
)

// runVerifyTemplateNames handles `whatomate verify-template-names`: for every
// WhatsApp account it fetches the templates from Meta (read-only GET) and lists
// local names that diverge from the approved ones. It never writes anything.
func runVerifyTemplateNames(args []string) {
	fs := flag.NewFlagSet("verify-template-names", flag.ExitOnError)
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
	client := whatsapp.New(logf.New(logf.Opts{}))

	var accounts []models.WhatsAppAccount
	if err := db.Find(&accounts).Error; err != nil {
		fatalf("%v", err)
	}
	total := 0
	for i := range accounts {
		acc := &accounts[i]
		acc.DecryptSecrets(cfg.App.EncryptionKey)
		metaTemplates, err := client.FetchTemplates(context.Background(), acc.ToWAAccount())
		if err != nil {
			fmt.Printf("account %s (%s): could not fetch from Meta: %v\n", acc.Name, acc.OrganizationID, err)
			continue
		}
		var local []models.Template
		if err := db.Where("organization_id = ? AND whats_app_account = ?", acc.OrganizationID, acc.Name).Find(&local).Error; err != nil {
			fatalf("%v", err)
		}
		for _, d := range templateaudit.Compare(local, metaTemplates) {
			total++
			fmt.Printf("account %s: template %s: %s local=%q meta=%q\n", acc.Name, d.TemplateID, d.Kind, d.Local, d.Meta)
		}
	}
	fmt.Printf("%d divergence(s) found (read-only; nothing was changed)\n", total)
}
