package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Exporting a case. The person picks sections and a format; the SERVER decides what each section may
// contain, from the same rules as the screens:
//
//	summary / history / documents list   anyone who can open the case (history withholds customer message text)
//	internal discussion                  those who may read the case discussion (team_chat:read + case access)
//	WhatsApp conversation                only those who may read it (canUseCaseChannel): never "because they see the case"
//	attached files (zip only)            only the files the person may download
//
// Asking for a section one may not have is refused (403), not quietly dropped; "full" means "everything
// I am allowed" and lists what was left out. Ids never come from the client: everything is read from the
// case in the path. The file is built in memory and sent: nothing is left on disk to clean up.
// Limits keep a request bounded; beyond them the answer is 413 (an asynchronous export is future work).

const (
	exportMaxEvents   = 5000
	exportMaxMessages = 5000
	exportMaxZipFiles = 100 << 20
)

var exportSections = map[string]bool{"summary": true, "history": true, "internal_chat": true, "whatsapp": true, "documents": true, "full": true}

type exportRequest struct {
	Sections []string `json:"sections"`
	Format   string   `json:"format"` // pdf | csv | zip
}

// exportAvailability says, for the caller, which sections can be exported.
func (a *App) exportAvailability(orgID, userID uuid.UUID, occ *models.Occurrence, acc occurrenceAccess) map[string]bool {
	var ch models.ChatChannel
	chatOK := false
	if a.HasPermission(userID, models.ResourceTeamChat, models.ActionRead, orgID) &&
		a.DB.Where("organization_id = ? AND occurrence_id = ? AND kind = ?", orgID, occ.ID, models.ChatChannelKindOccurrence).First(&ch).Error == nil {
		chatOK = a.canAccessChatChannelDB(a.DB, userID, orgID, &ch)
	}
	return map[string]bool{
		"summary": true, "history": true, "documents": true,
		"internal_chat": chatOK,
		"whatsapp":      a.canUseCaseChannel(orgID, userID, occ, acc, a.caseChannelAccount(orgID, occ), false),
	}
}

// GetOccurrenceExportOptions tells the UI what to offer.
func (a *App) GetOccurrenceExportOptions(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	return r.SendEnvelope(map[string]any{"sections": a.exportAvailability(orgID, userID, occ, acc)})
}

type exportTable struct {
	name   string
	header []string
	rows   [][]string
}

func tsv(t time.Time) string { return t.Local().Format("02/01/2006 15:04") }

// ExportOccurrence builds the report.
func (a *App) ExportOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	var req exportRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Format != "pdf" && req.Format != "csv" && req.Format != "zip" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "format must be pdf, csv or zip", nil, "")
	}
	want := map[string]bool{}
	for _, s := range req.Sections {
		if !exportSections[s] {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "unknown section", nil, "")
		}
		want[s] = true
	}
	if len(want) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "choose at least one section", nil, "")
	}
	avail := a.exportAvailability(orgID, userID, occ, acc)
	var omitted []string
	if want["full"] {
		for s, ok := range avail {
			if ok {
				want[s] = true
			} else {
				omitted = append(omitted, s)
			}
		}
		delete(want, "full")
	}
	for s := range want {
		if !avail[s] {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot export this section", nil, fastglue.ErrorType("forbidden_section"))
		}
	}
	if !a.chatUploadAllowed(userID) { // same per-person throttle as uploads: an export is heavy
		return a.sendChatError(r, errChatRateLimited, "export the case")
	}

	// Load the case with its names, as the screen does.
	a.DB.Preload("Stage").Preload("AssignedUser").Preload("Unit").Preload("Category").Preload("Contact").First(occ, occ.ID)
	teamName := ""
	if occ.TeamID != nil {
		a.DB.Model(&models.Team{}).Where("id = ? AND organization_id = ?", *occ.TeamID, orgID).Select("name").Scan(&teamName)
	}

	var tables []exportTable
	counts := map[string]int{}
	if want["history"] {
		t, ok := a.exportHistory(orgID, userID, occ, acc)
		if !ok {
			return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge, "The history is too large to export at once", nil, "")
		}
		tables = append(tables, t)
		counts["history"] = len(t.rows)
	}
	if want["internal_chat"] {
		t, ok := a.exportInternalChat(orgID, occ)
		if !ok {
			return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge, "The discussion is too large to export at once", nil, "")
		}
		tables = append(tables, t)
		counts["internal_chat"] = len(t.rows)
	}
	if want["whatsapp"] {
		t, ok := a.exportWhatsApp(orgID, occ)
		if !ok {
			return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge, "The conversation is too large to export at once", nil, "")
		}
		tables = append(tables, t)
		counts["whatsapp"] = len(t.rows)
	}
	var files []models.OccurrenceFile
	if want["documents"] {
		a.DB.Where("organization_id = ? AND occurrence_id = ?", orgID, occ.ID).Order("created_at ASC").Find(&files)
		t := exportTable{name: "documents", header: []string{"Nome", "Tipo", "Tamanho (bytes)", "Enviado por", "Data", "Restrito"}}
		ids := make([]uuid.UUID, len(files))
		for i, f := range files {
			ids[i] = f.UploadedByID
		}
		names := a.chatUserNames(ids)
		for _, f := range files {
			t.rows = append(t.rows, []string{f.Name, f.Mime, fmt.Sprint(f.SizeBytes), names[f.UploadedByID], tsv(f.CreatedAt), map[bool]string{true: "sim", false: "não"}[f.Restricted]})
		}
		tables = append(tables, t)
		counts["documents"] = len(files)
	}

	stamp := time.Now().Format("20060102-150405")
	base := "protocolo-" + strings.ReplaceAll(occ.ProtocolNumber, "/", "-") + "-" + stamp
	var body []byte
	var ctype, filename string

	switch req.Format {
	case "csv":
		if len(tables) != 1 || want["summary"] {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "CSV holds one table: choose a single section among history, discussion, WhatsApp or documents (or use PDF or ZIP)", nil, "")
		}
		body, filename, ctype = csvBytes(tables[0]), base+"-"+tables[0].name+".csv", "text/csv; charset=utf-8"
	case "pdf":
		body, filename, ctype = a.exportPDF(occ, teamName, want, tables, omitted, files), base+".pdf", "application/pdf"
	case "zip":
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		put := func(name string, data []byte) {
			if w, e := zw.Create(name); e == nil {
				_, _ = w.Write(data)
			}
		}
		put("relatorio.pdf", a.exportPDF(occ, teamName, want, tables, omitted, files))
		for _, t := range tables {
			put(t.name+".csv", csvBytes(t))
		}
		total := 0
		for _, f := range files {
			if !a.canDownloadOccurrenceFile(userID, acc, &f) {
				continue
			}
			full, perr := a.resolveSafeStoragePath(f.StoragePath)
			if perr != nil {
				continue
			}
			data, rerr := os.ReadFile(full)
			if rerr != nil {
				continue
			}
			if total += len(data); total > exportMaxZipFiles {
				return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge, "The documents are too large to export at once", nil, "")
			}
			put("documentos/"+f.ID.String()[:8]+"-"+sanitizeChatFileName(f.Name, ""), data)
		}
		_ = zw.Close()
		body, filename, ctype = buf.Bytes(), base+".zip", "application/zip"
	}

	h := &r.RequestCtx.Response.Header
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	r.RequestCtx.SetBody(body)
	secs := make([]string, 0, len(want))
	for s := range want {
		secs = append(secs, s)
	}
	a.logAudit(orgID, userID, "occurrence_export", occ.ID, models.AuditActionAccessed, nil,
		map[string]any{"protocol": occ.ProtocolNumber, "format": req.Format, "sections": secs, "omitted": omitted, "counts": counts})
	return nil
}

func csvBytes(t exportTable) []byte {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // BOM so Excel reads accents
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	_ = w.Write(t.header)
	for _, row := range t.rows {
		// A cell that starts with = + - @ would run as a formula in a spreadsheet: neutralize it.
		safe := make([]string, len(row))
		for i, c := range row {
			if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
				c = "'" + c
			}
			safe[i] = c
		}
		_ = w.Write(safe)
	}
	w.Flush()
	return buf.Bytes()
}

func (a *App) exportHistory(orgID, userID uuid.UUID, occ *models.Occurrence, acc occurrenceAccess) (exportTable, bool) {
	t := exportTable{name: "historico", header: []string{"Data", "Evento", "Autor", "Descrição"}}
	reads := a.canUseCaseChannel(orgID, userID, occ, acc, a.caseChannelAccount(orgID, occ), false)
	var events []models.OccurrenceEvent
	a.DB.Where("organization_id = ? AND occurrence_id = ?", orgID, occ.ID).
		Where(`NOT (type = 'stage_change' AND EXISTS (
			SELECT 1 FROM occurrence_events s
			WHERE s.occurrence_id = occurrence_events.occurrence_id AND s.deleted_at IS NULL AND s.type IN ('closed','reopened')
			  AND s.created_by_id IS NOT DISTINCT FROM occurrence_events.created_by_id
			  AND ABS(EXTRACT(EPOCH FROM (s.created_at - occurrence_events.created_at))) < 2))`).
		Preload("CreatedBy").Order("created_at ASC, id ASC").Limit(exportMaxEvents + 1).Find(&events)
	if len(events) > exportMaxEvents {
		return t, false
	}
	for _, e := range events {
		content := e.Content
		if !reads && (e.Type == models.OccurrenceEventReply || e.Type == models.OccurrenceEventProcessMessageUsed) {
			content = "(texto da mensagem ao cliente não disponível para o seu perfil)"
		}
		author := ""
		if e.CreatedBy != nil {
			author = e.CreatedBy.FullName
		}
		t.rows = append(t.rows, []string{tsv(e.CreatedAt), string(e.Type), author, content})
	}
	return t, true
}

func (a *App) exportInternalChat(orgID uuid.UUID, occ *models.Occurrence) (exportTable, bool) {
	t := exportTable{name: "discussao-interna", header: []string{"Data", "Autor", "Mensagem", "Anexos"}}
	var ch models.ChatChannel
	if a.DB.Where("organization_id = ? AND occurrence_id = ? AND kind = ?", orgID, occ.ID, models.ChatChannelKindOccurrence).First(&ch).Error != nil {
		return t, true
	}
	var msgs []models.ChatMessage
	a.DB.Where("channel_id = ? AND organization_id = ? AND kind = ?", ch.ID, orgID, models.ChatMessageKindUser).
		Order("created_at ASC, id ASC").Limit(exportMaxMessages + 1).Find(&msgs)
	if len(msgs) > exportMaxMessages {
		return t, false
	}
	ids := make([]uuid.UUID, len(msgs))
	mids := make([]uuid.UUID, len(msgs))
	for i, m := range msgs {
		ids[i], mids[i] = m.UserID, m.ID
	}
	names := a.chatUserNames(ids)
	files := a.chatAttachmentsFor(mids)
	for _, m := range msgs {
		var fn []string
		for _, f := range files[m.ID] {
			fn = append(fn, f.Name)
		}
		t.rows = append(t.rows, []string{tsv(m.CreatedAt), names[m.UserID], m.Body, strings.Join(fn, ", ")})
	}
	return t, true
}

func (a *App) exportWhatsApp(orgID uuid.UUID, occ *models.Occurrence) (exportTable, bool) {
	t := exportTable{name: "whatsapp", header: []string{"Data", "Sentido", "Mensagem", "Estado", "Enviado por"}}
	account := a.caseChannelAccount(orgID, occ)
	if account == nil {
		return t, true
	}
	var rows []models.Message
	a.DB.Where("organization_id = ? AND contact_id = ? AND whats_app_account = ? AND created_at >= ?",
		orgID, occ.ContactID, account.Name, occ.OpenedAt.Add(-time.Minute)).
		Order("created_at ASC").Limit(exportMaxMessages + 1).Find(&rows)
	if len(rows) > exportMaxMessages {
		return t, false
	}
	for _, m := range rows {
		who := ""
		if m.SentByUserID != nil {
			who = a.senderNameForBroadcast(&m)
		}
		dir := "recebida"
		if m.Direction == models.DirectionOutgoing {
			dir = "enviada"
		}
		t.rows = append(t.rows, []string{tsv(m.CreatedAt), dir, m.Content, string(m.Status), who})
	}
	return t, true
}

func (a *App) exportPDF(occ *models.Occurrence, team string, want map[string]bool, tables []exportTable, omitted []string, files []models.OccurrenceFile) []byte {
	now := time.Now()
	d := newPDF("Protocolo "+occ.ProtocolNumber+" - "+occ.Title, "Gerado em "+now.Format("02/01/2006 15:04"))
	d.put("Protocolo "+occ.ProtocolNumber, 16, true, 0)
	d.put(occ.Title, 11, false, 0)
	d.space(4)
	if want["summary"] {
		d.heading("Resumo")
		stage := ""
		if occ.Stage != nil {
			stage = occ.Stage.Name
		}
		d.kv("Cliente", func() string {
			if occ.Contact != nil {
				return occ.Contact.ProfileName
			}
			return ""
		}())
		d.kv("Etapa", stage)
		d.kv("Estado geral", string(occ.GeneralState))
		d.kv("Prioridade", string(occ.Priority))
		if occ.Unit != nil {
			d.kv("Unidade/Loja", occ.Unit.Name)
		}
		d.kv("Equipe", team)
		if occ.AssignedUser != nil {
			d.kv("Responsável", occ.AssignedUser.FullName)
		}
		if occ.Category != nil {
			d.kv("Categoria", occ.Category.Name)
		}
		d.kv("Pedido", occ.OrderRef)
		d.kv("Aberto em", tsv(occ.OpenedAt))
		if occ.ClosedAt != nil {
			d.kv("Concluído em", tsv(*occ.ClosedAt))
		}
		if strings.TrimSpace(occ.Description) != "" {
			d.space(3)
			d.paragraph(occ.Description, 0)
		}
	}
	titles := map[string]string{"historico": "Histórico", "discussao-interna": "Discussão interna", "whatsapp": "Conversa WhatsApp", "documents": "Documentos"}
	for _, t := range tables {
		d.heading(fmt.Sprintf("%s (%d)", titles[t.name], len(t.rows)))
		if len(t.rows) == 0 {
			d.put("Nenhum registro.", 9.5, false, 0)
		}
		for _, row := range t.rows {
			switch t.name {
			case "documents":
				d.put(fmt.Sprintf("%s - %s - %s bytes - %s - %s", row[0], row[1], row[2], row[3], row[4]), 9, false, 0)
			default:
				d.put(strings.TrimSpace(row[0]+"  "+row[1]), 9, true, 0)
				body := row[len(row)-1]
				if t.name == "whatsapp" {
					body = row[2]
				}
				if t.name == "discussao-interna" {
					body = row[2]
					if row[3] != "" {
						body += "  [anexos: " + row[3] + "]"
					}
				}
				if t.name == "historico" {
					body = strings.TrimSpace(row[2] + ": " + row[3])
				}
				d.paragraph(body, 10)
			}
		}
	}
	if len(omitted) > 0 {
		d.heading("Seções não incluídas")
		d.paragraph("Por falta de permissão, as seguintes seções não fazem parte deste relatório: "+strings.Join(omitted, ", ")+".", 0)
	}
	return d.bytes()
}
