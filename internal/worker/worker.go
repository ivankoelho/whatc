package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// Worker processes jobs from the queue
type Worker struct {
	Config    *config.Config
	DB        *gorm.DB
	Redis     *redis.Client
	Log       logf.Logger
	WhatsApp  *whatsapp.Client
	Consumer  *queue.RedisConsumer
	Publisher *queue.Publisher
	// Usage records the WhatsApp consumption ledger (fail-open; nil or disabled = nothing).
	Usage *usage.Recorder
	// Now overrides the clock (tests); nil = time.Now.
	Now func() time.Time
}

// Ensure Worker implements JobHandler interface
var _ queue.JobHandler = (*Worker)(nil)

// New creates a new Worker instance
func New(cfg *config.Config, db *gorm.DB, rdb *redis.Client, log logf.Logger) (*Worker, error) {
	consumer, err := queue.NewRedisConsumer(rdb, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	publisher := queue.NewPublisher(rdb, log)

	return &Worker{
		Config:    cfg,
		DB:        db,
		Redis:     rdb,
		Log:       log,
		WhatsApp:  whatsapp.New(log),
		Consumer:  consumer,
		Publisher: publisher,
		Usage:     usage.New(db, cfg.Usage),
	}, nil
}

// Run starts the worker and processes jobs until context is cancelled
func (w *Worker) Run(ctx context.Context) error {
	w.Log.Info("Worker starting")

	// Retention: sealed verification codes are dropped after their TTL.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := ScrubExpiredSecrets(w.DB, w.now(), w.otpPolicy()); err != nil {
				w.Log.Error("Failed to scrub expired verification codes", "error", err)
			} else if n > 0 {
				w.Log.Info("Scrubbed expired verification codes", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	err := w.Consumer.Consume(ctx, w)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("consumer error: %w", err)
	}

	w.Log.Info("Worker stopped")
	return nil
}

// HandleRecipientJob processes a single recipient message job
func (w *Worker) HandleRecipientJob(ctx context.Context, job *queue.RecipientJob) error {
	// Check if campaign is still active before sending
	var campaign models.BulkMessageCampaign
	if err := w.DB.Where("id = ?", job.CampaignID).Preload("Template").First(&campaign).Error; err != nil {
		w.Log.Error("Failed to load campaign", "error", err, "campaign_id", job.CampaignID)
		return fmt.Errorf("failed to load campaign: %w", err)
	}

	// Skip if campaign is paused or cancelled
	if campaign.Status == models.CampaignStatusPaused || campaign.Status == models.CampaignStatusCancelled {
		w.Log.Info("Campaign not active, skipping recipient", "campaign_id", job.CampaignID, "status", campaign.Status, "recipient_id", job.RecipientID)
		return nil // Not an error, just skip
	}

	// Get WhatsApp account
	var account models.WhatsAppAccount
	if err := w.DB.Where("name = ? AND organization_id = ?", campaign.WhatsAppAccount, job.OrganizationID).First(&account).Error; err != nil {
		w.Log.Error("Failed to load WhatsApp account", "error", err, "account_name", campaign.WhatsAppAccount)
		w.updateRecipientStatus(job.RecipientID, models.MessageStatusFailed, "", "WhatsApp account not found")
		w.incrementCampaignCount(job.CampaignID, "failed_count")
		return nil // Don't retry, mark as failed
	}
	w.decryptAccountSecrets(&account)

	// Get or create contact for this recipient
	contact, _, err := contactutil.GetOrCreateContact(w.DB, job.OrganizationID, job.PhoneNumber, job.RecipientName)
	if err != nil || contact == nil {
		w.Log.Error("Failed to get or create contact", "error", err, "phone", job.PhoneNumber)
		w.updateRecipientStatus(job.RecipientID, models.MessageStatusFailed, "", "Failed to create contact")
		w.incrementCampaignCount(job.CampaignID, "failed_count")
		return nil // Don't retry
	}

	// Check marketing opt-out
	if contact.MarketingOptOut && campaign.Template != nil && strings.EqualFold(campaign.Template.Category, "MARKETING") {
		w.Log.Info("Skipping marketing message for opted-out contact", "contact_id", contact.ID, "phone", job.PhoneNumber)
		w.updateRecipientStatus(job.RecipientID, models.MessageStatusFailed, "", "Contact opted out of marketing messages")
		w.incrementCampaignCount(job.CampaignID, "failed_count")
		return nil
	}

	// One-time codes travel sealed (queue and database). Open them only here,
	// in memory, right before the send.
	params := job.TemplateParams
	isAuth := campaign.Template != nil && templateutil.IsAuthCategory(campaign.Template.Category)
	secret := isAuth || templateutil.IsSealed(params)
	if secret {
		// A job redelivered after a successful send (crash before ACK) must
		// neither resend the code nor fail the recipient.
		var st string
		w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", job.RecipientID).Pluck("status", &st)
		if st == string(models.MessageStatusSent) || st == string(models.MessageStatusDelivered) || st == string(models.MessageStatusRead) {
			return nil
		}
		var key string
		if w.Config != nil {
			key = w.Config.App.EncryptionKey
		}
		sealedParams := params
		opened, oerr := templateutil.OpenParams(params, key, w.now())
		if oerr != nil || len(opened) == 0 {
			reason := "otp_unavailable: the verification code cannot be decrypted"
			if errors.Is(oerr, templateutil.ErrSealExpired) || (oerr == nil && len(opened) == 0) {
				reason = "otp_expired: the verification code is no longer available"
			}
			w.Log.Error("Verification code unavailable for recipient", "recipient_id", job.RecipientID, "campaign_id", job.CampaignID)
			w.failOTPRecipient(job, reason)
			return nil
		}
		// Validity is checked as late as possible: right before the Meta call.
		// A retry sees the same envelope, so it can never renew the validity.
		if cerr := w.otpPolicy().Check(sealedParams, w.now()); cerr != nil {
			w.Log.Warn("Verification code not sent", "reason", cerr.Error(), "recipient_id", job.RecipientID, "campaign_id", job.CampaignID)
			w.failOTPRecipient(job, cerr.Error()+": the verification code is not valid for sending")
			return nil
		}
		params = opened
	}

	// Build recipient for sending
	recipient := &models.BulkMessageRecipient{
		PhoneNumber:    job.PhoneNumber,
		RecipientName:  job.RecipientName,
		TemplateParams: params,
		HeaderParams:   job.HeaderParams,
	}

	// Send template message
	waMessageID, err := w.sendTemplateMessage(ctx, &account, campaign.Template, recipient, campaign.HeaderMediaID, campaign.HeaderMediaFilename)
	if err != nil && secret {
		// The error text may echo request details: never let a code reach
		// logs, the recipient row or the message row.
		err = errors.New(templateutil.RedactValues(err.Error(), params))
	}
	sendErr := err // kept for the usage ledger: err is shadowed below

	// Create Message record
	message := models.Message{
		OrganizationID:    job.OrganizationID,
		WhatsAppAccount:   campaign.WhatsAppAccount,
		ContactID:         contact.ID,
		WhatsAppMessageID: waMessageID,
		Direction:         models.DirectionOutgoing,
		MessageType:       models.MessageTypeTemplate,
		TemplateParams:    storedParams(secret, job.TemplateParams),
		Metadata: models.JSONB{
			"campaign_id":    job.CampaignID.String(),
			"recipient_name": job.RecipientName,
		},
	}
	if campaign.Template != nil {
		message.TemplateName = campaign.Template.Name
		content := templateutil.ReplaceWithJSONBParams(campaign.Template.BodyContent, campaign.Template.BodyContent, params)
		if secret {
			// One-time codes are never persisted with the message history.
			content = templateutil.MaskAllParams(campaign.Template.BodyContent)
			message.TemplateParams = nil
			message.Metadata["auth_code_masked"] = true
		}
		message.Content = content
		// Store campaign header media so it renders in the chat bubble
		if campaign.HeaderMediaLocalPath != "" {
			message.MediaURL = campaign.HeaderMediaLocalPath
			message.MediaMimeType = campaign.HeaderMediaMimeType
			message.MediaFilename = campaign.HeaderMediaFilename
		}
	}

	if err != nil {
		w.Log.Error("Failed to send message", "error", err, "recipient", job.PhoneNumber)
		message.Status = models.MessageStatusFailed
		message.ErrorMessage = err.Error()
		w.updateRecipientStatus(job.RecipientID, models.MessageStatusFailed, "", err.Error())
		w.incrementCampaignCount(job.CampaignID, "failed_count")
	} else {
		w.Log.Info("Message sent", "recipient", job.PhoneNumber, "message_id", waMessageID)
		message.Status = models.MessageStatusSent
		w.updateRecipientStatus(job.RecipientID, models.MessageStatusSent, waMessageID, "")
		if secret {
			w.scrubRecipientSecret(job.RecipientID) // sent: the code is no longer needed
		}
		w.incrementCampaignCount(job.CampaignID, "sent_count")
	}

	// Save message record
	if err := w.DB.Create(&message).Error; err != nil {
		w.Log.Error("Failed to save message", "error", err, "recipient", job.PhoneNumber)
	} else {
		w.recordCampaignUsage(ctx, &message, job.CampaignID, sendErr)
	}

	// Check if campaign is complete (all recipients processed)
	w.checkCampaignCompletion(ctx, job.CampaignID, job.OrganizationID)

	return nil
}

// updateRecipientStatus updates the recipient's status in the database
func (w *Worker) updateRecipientStatus(recipientID uuid.UUID, status models.MessageStatus, waMessageID, errorMsg string) {
	updates := map[string]any{
		"status":               status,
		"whats_app_message_id": waMessageID,
	}
	if status == models.MessageStatusSent {
		updates["sent_at"] = time.Now()
	}
	if errorMsg != "" {
		updates["error_message"] = errorMsg
	}
	w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", recipientID).Updates(updates)
}

// incrementCampaignCount increments a campaign counter atomically
func (w *Worker) incrementCampaignCount(campaignID uuid.UUID, column string) {
	w.DB.Model(&models.BulkMessageCampaign{}).
		Where("id = ?", campaignID).
		Update(column, gorm.Expr(column+" + 1"))
}

// publishCampaignStats publishes campaign stats for real-time updates
func (w *Worker) publishCampaignStats(ctx context.Context, campaignID, organizationID uuid.UUID) {
	var campaign models.BulkMessageCampaign
	if err := w.DB.Where("id = ?", campaignID).First(&campaign).Error; err != nil {
		return
	}

	_ = w.Publisher.PublishCampaignStats(ctx, &queue.CampaignStatsUpdate{
		CampaignID:     campaignID.String(),
		OrganizationID: organizationID,
		Status:         campaign.Status,
		SentCount:      campaign.SentCount,
		DeliveredCount: campaign.DeliveredCount,
		ReadCount:      campaign.ReadCount,
		FailedCount:    campaign.FailedCount,
	})
}

// checkCampaignCompletion checks if all recipients are processed and marks campaign as completed
func (w *Worker) checkCampaignCompletion(ctx context.Context, campaignID, organizationID uuid.UUID) {
	// Count pending recipients
	var pendingCount int64
	w.DB.Model(&models.BulkMessageRecipient{}).
		Where("campaign_id = ? AND status = ?", campaignID, models.MessageStatusPending).
		Count(&pendingCount)

	// If no pending recipients, mark campaign as completed
	if pendingCount == 0 {
		var campaign models.BulkMessageCampaign
		if err := w.DB.Where("id = ?", campaignID).First(&campaign).Error; err != nil {
			return
		}

		// Only complete if currently processing
		if campaign.Status != models.CampaignStatusProcessing {
			return
		}

		now := time.Now()
		w.DB.Model(&campaign).Updates(map[string]any{
			"status":       models.CampaignStatusCompleted,
			"completed_at": now,
		})

		w.Log.Info("Campaign completed", "campaign_id", campaignID, "sent", campaign.SentCount, "failed", campaign.FailedCount)

		// Publish completion status
		_ = w.Publisher.PublishCampaignStats(ctx, &queue.CampaignStatsUpdate{
			CampaignID:     campaignID.String(),
			OrganizationID: organizationID,
			Status:         models.CampaignStatusCompleted,
			SentCount:      campaign.SentCount,
			DeliveredCount: campaign.DeliveredCount,
			ReadCount:      campaign.ReadCount,
			FailedCount:    campaign.FailedCount,
		})
	} else {
		// Publish current stats
		w.publishCampaignStats(ctx, campaignID, organizationID)
	}
}

// sendTemplateMessage sends a template message via WhatsApp Cloud API
func (w *Worker) sendTemplateMessage(ctx context.Context, account *models.WhatsAppAccount, template *models.Template, recipient *models.BulkMessageRecipient, campaignHeaderMediaID, campaignHeaderMediaFilename string) (string, error) {
	waAccount := account.ToWAAccount()

	// Resolve body parameters into a map for BuildTemplateComponents
	resolvedParams := templateutil.ResolveParams(template.BodyContent, recipient.TemplateParams)
	bodyParams := make(map[string]string, len(resolvedParams))
	paramNames := templateutil.ExtParamNames(template.BodyContent)
	for i, val := range resolvedParams {
		if i < len(paramNames) {
			bodyParams[paramNames[i]] = val
		} else {
			bodyParams[fmt.Sprintf("%d", i+1)] = val
		}
	}

	// Resolve the header text parameter (if any) into its own map. Prefer
	// recipient.HeaderParams (new path, populated by AddRecipients) and fall
	// back to a TemplateParams lookup for legacy recipient rows persisted
	// before HeaderParams existed.
	var headerParams map[string]string
	if template.HeaderType == "TEXT" {
		if hNames := templateutil.ExtParamNames(template.HeaderContent); len(hNames) == 1 {
			name := hNames[0]
			if raw, ok := recipient.HeaderParams[name]; ok {
				headerParams = map[string]string{name: fmt.Sprintf("%v", raw)}
			} else if raw, ok := recipient.TemplateParams[name]; ok {
				headerParams = map[string]string{name: fmt.Sprintf("%v", raw)}
			}
		}
	}

	// Use the shared component builder (same as chat template sending).
	components, err := whatsapp.BuildTemplateComponents(
		bodyParams,
		template.HeaderType, template.HeaderContent,
		headerParams,
		campaignHeaderMediaID, campaignHeaderMediaFilename,
	)
	if err != nil {
		return "", fmt.Errorf("failed to build template components: %w", err)
	}
	// Add auto-generated button components (Flow needs flow_token)
	flowComponents := whatsapp.AutoButtonComponents(template.Buttons)
	components = append(components, flowComponents...)

	// Authentication templates: Meta also wants the code on the OTP button
	// (same rule and helper as the direct send in handlers.SendTemplateMessage).
	if templateutil.IsAuthCategory(template.Category) && len(resolvedParams) > 0 {
		code := resolvedParams[0]
		if v, ok := recipient.TemplateParams["1"]; ok {
			code = fmt.Sprintf("%v", v)
		}
		components = append(components,
			whatsapp.ButtonURLParamsToComponents(whatsapp.OTPButtonParams(template.Buttons, code), template.Buttons)...)
	}

	if templateutil.IsAuthCategory(template.Category) {
		// The client redacts echoed codes from errors before any logging.
		ctx = whatsapp.WithSecrets(ctx, resolvedParams...)
	}

	rcpt := whatsapp.Recipient{Phone: recipient.PhoneNumber}
	return w.WhatsApp.SendTemplateMessage(ctx, waAccount, rcpt, template.Name, template.Language, components)
}

// decryptAccountSecrets decrypts the encrypted secrets on a WhatsApp account.
func (w *Worker) decryptAccountSecrets(account *models.WhatsAppAccount) {
	var key string
	if w.Config != nil {
		key = w.Config.App.EncryptionKey
	}
	account.DecryptSecrets(key)
}

// Close cleans up worker resources
func (w *Worker) Close() error {
	if w.Consumer != nil {
		return w.Consumer.Close()
	}
	return nil
}

// recordCampaignUsage writes the ledger row of a campaign message, which the
// worker only creates after the send. It is fail-open: nothing here may fail or
// stall the campaign, so errors and panics are logged and swallowed.
func (w *Worker) recordCampaignUsage(ctx context.Context, msg *models.Message, campaignID uuid.UUID, sendErr error) {
	if !w.Usage.Enabled() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			w.Log.Error("Usage recording panicked", "panic", fmt.Sprint(r))
		}
	}()
	origin := usage.Origin{ActorType: usage.ActorCampaign, CampaignID: &campaignID, Detail: "campaign"}
	if err := w.Usage.Record(ctx, nil, msg, origin); err != nil {
		w.Log.Error("Usage recording failed", "op", "record", "error", err, "message_id", msg.ID)
		return
	}
	var err error
	switch {
	case sendErr != nil:
		err = w.Usage.MarkSendFailed(ctx, nil, msg.OrganizationID, msg.ID)
	case msg.WhatsAppMessageID != "":
		err = w.Usage.AttachWamid(ctx, msg.OrganizationID, msg.ID, msg.WhatsAppAccount, msg.WhatsAppMessageID)
	}
	if err != nil {
		w.Log.Error("Usage recording failed", "op", "settle", "error", err, "message_id", msg.ID)
	}
}

// storedParams is what a message row may keep of the recipient's params:
// nothing at all when they hold a one-time code.
func storedParams(secret bool, params models.JSONB) models.JSONB {
	if secret {
		return nil
	}
	return params
}

// scrubRecipientSecret irreversibly drops a recipient's stored params.
func (w *Worker) scrubRecipientSecret(recipientID uuid.UUID) {
	if err := w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", recipientID).
		UpdateColumn("template_params", models.JSONB{}).Error; err != nil {
		w.Log.Error("Failed to scrub recipient secret", "error", err, "recipient_id", recipientID)
	}
}

// ScrubExpiredSecrets drops sealed codes older than templateutil.SealedTTL,
// whatever the recipient's state (a failed recipient keeps its code only for
// the retry window).
func ScrubExpiredSecrets(db *gorm.DB, now time.Time, policy templateutil.OTPPolicy) (int64, error) {
	maxSecs := int64(policy.MaxValidity / time.Second)
	if maxSecs <= 0 {
		maxSecs = int64(templateutil.DefaultOTPMaxValidity / time.Second)
	}
	// Same rule as OTPPolicy.Expired: a code with the issuer's valid_until goes at
	// min(valid_until, sealed_at+max); one without it only at the retention limit,
	// so pending legacy campaigns keep their (still sendable) codes.
	res := db.Unscoped().Model(&models.BulkMessageRecipient{}).
		Where(`template_params->>@sealed IS NOT NULL AND CASE
			WHEN template_params->>@until IS NOT NULL THEN
				LEAST((template_params->>@until)::bigint, COALESCE((template_params->>@at)::bigint + @max, (template_params->>@until)::bigint)) <= @now
			ELSE COALESCE((template_params->>@at)::bigint, 0) < @cutoff END`,
			sql.Named("sealed", templateutil.SealedKey), sql.Named("until", templateutil.SealedValidUntilKey),
			sql.Named("at", templateutil.SealedAtKey), sql.Named("max", maxSecs),
			sql.Named("now", now.Unix()), sql.Named("cutoff", now.Add(-templateutil.SealedTTL).Unix())).
		UpdateColumn("template_params", models.JSONB{})
	return res.RowsAffected, res.Error
}

// now is the worker clock (injectable in tests).
func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// otpPolicy builds the OTP validity policy from configuration.
func (w *Worker) otpPolicy() templateutil.OTPPolicy {
	if w.Config == nil {
		return templateutil.OTPPolicy{}
	}
	return templateutil.OTPPolicy{
		MaxValidity:       w.Config.OTP.MaxValidity(),
		RequireValidUntil: w.Config.OTP.RequireValidUntil,
	}
}

// failOTPRecipient ends a recipient whose code cannot be sent: it is marked
// failed with the reason and its stored secret is dropped.
func (w *Worker) failOTPRecipient(job *queue.RecipientJob, reason string) {
	w.updateRecipientStatus(job.RecipientID, models.MessageStatusFailed, "", reason)
	w.scrubRecipientSecret(job.RecipientID)
	w.incrementCampaignCount(job.CampaignID, "failed_count")
	w.checkCampaignCompletion(context.Background(), job.CampaignID, job.OrganizationID)
}
