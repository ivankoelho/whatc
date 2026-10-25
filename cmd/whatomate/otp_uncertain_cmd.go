package main

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/internal/worker"
)

// runOTPUncertain handles `whatomate otp-uncertain`. Without -release it only LISTS the
// authentication-campaign recipients whose delivery is uncertain (read-only), including
// claims that are stuck and not yet resolved by a worker.
//
// -release drops the claim of ONE recipient so retry-failed may send it again. It is a
// deliberate, attributed act: it needs -apply, -confirm-not-delivered, -operator (the
// e-mail of a real administrator of the campaign's organization) and -reason, and it
// refuses when the code is no longer valid. It audits the release in the same
// transaction and queues nothing. Doing it without checking can deliver the same code twice.
func runOTPUncertain(args []string) {
	fs := flag.NewFlagSet("otp-uncertain", flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config file")
	release := fs.String("release", "", "Recipient id to release after reconciliation")
	apply := fs.Bool("apply", false, "Write the release (default: check and report only)")
	confirm := fs.Bool("confirm-not-delivered", false, "Declare that you verified the code was NOT delivered to the customer")
	operator := fs.String("operator", "", "E-mail of the administrator declared as releasing it (a real user of the campaign's organization; NOT authenticated: the audit records a declared operator)")
	reason := fs.String("reason", "", "Why the code is known not to have been delivered (audited; never write a code here)")
	_ = fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("failed to load config: %v", err)
	}
	db, err := database.NewPostgres(&cfg.Database, false)
	if err != nil {
		fatalf("failed to connect to the database: %v", err)
	}

	if *release == "" {
		list, err := authmask.ListUncertain(db, worker.DefaultClaimStale())
		if err != nil {
			fatalf("%v", err)
		}
		for _, u := range list {
			fmt.Printf("[%s] recipient %s campaign %s %q phone ...%s message %s since %s\n  %s\n",
				u.State, u.RecipientID, u.CampaignID, u.CampaignName, u.PhoneLast4, u.MessageID, u.Since.UTC().Format(time.RFC3339), u.Reason)
		}
		fmt.Printf("%d recipient(s) with an uncertain delivery (read-only; nothing changed)\n", len(list))
		return
	}

	id, err := uuid.Parse(*release)
	if err != nil {
		fatalf("-release must be a recipient UUID")
	}
	if !*confirm {
		fatalf("-confirm-not-delivered is required: confirm with Meta's logs or the customer that the code was NOT delivered")
	}
	if *operator == "" {
		fatalf("-operator is required: the e-mail of a real administrator of the campaign's organization")
	}
	policy := templateutil.OTPPolicy{MaxValidity: cfg.OTP.MaxValidity(), RequireValidUntil: cfg.OTP.RequireValidUntil}
	err = authmask.ReleaseUncertain(db, authmask.ReleaseRequest{RecipientID: id, OperatorEmail: *operator, Reason: *reason}, policy, time.Now(), !*apply)
	switch {
	case errors.Is(err, authmask.ErrNotUncertain), errors.Is(err, authmask.ErrOperatorNotAllowed), errors.Is(err, authmask.ErrInvalidReason),
		errors.Is(err, authmask.ErrNoCodeStored), errors.Is(err, authmask.ErrCodeNotValid):
		fatalf("refused: %v", err)
	case err != nil:
		fatalf("%v", err)
	}
	if !*apply {
		fmt.Printf("Dry run: every check passed; would release recipient %s (nothing written). Add -apply to do it.\n", id)
		return
	}
	fmt.Printf("Released recipient %s and audited it under the DECLARED operator %s (the identity is not authenticated). retry-failed may now send it again; nothing was queued.\n", id, *operator)
}
