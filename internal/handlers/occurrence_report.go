package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// The case report (PDF). It lists what the system actually recorded about the case and who asked for the
// report: parties, the chronological history, files, and the issuance record. It does not claim a digital
// signature, a read receipt or tamper-proofing, because none of those exist: the reference code only ties the
// document to the audit-log entry written when it was issued.

type reportParty struct{ role, name, department string }

type reportData struct {
	occ         *models.Occurrence
	org         string
	team        string
	loc         *time.Location
	issuedBy    string
	issuedAt    time.Time
	ref         string
	parties     []reportParty
	withSummary bool
}

var refDigits = regexp.MustCompile(`[^0-9A-Za-z]+`)

// reportReference builds the code printed on the report: it is also written to the audit log with the
// issuance, so the document can be looked up there. Not a signature.
func reportReference(orgID, occID, userID uuid.UUID, protocol string, at time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d", orgID, occID, userID, at.UnixNano())))
	return fmt.Sprintf("WHC-%s-%s-%s", strings.ToUpper(refDigits.ReplaceAllString(protocol, "")), at.UTC().Format("20060102150405"), strings.ToUpper(hex.EncodeToString(sum[:])[:4]))
}

func (a *App) newReportData(orgID, userID uuid.UUID, occ *models.Occurrence, team string, loc *time.Location, at time.Time, withSummary bool) reportData {
	rd := reportData{occ: occ, team: team, loc: loc, issuedAt: at, withSummary: withSummary,
		ref: reportReference(orgID, occ.ID, userID, occ.ProtocolNumber, at)}
	var org models.Organization
	if a.DB.Select("name").Where("id = ?", orgID).First(&org).Error == nil {
		rd.org = org.Name
	}
	if rd.org == "" {
		rd.org = "WhatC"
	}
	names := a.chatUserNames([]uuid.UUID{userID})
	rd.issuedBy = names[userID]
	if !withSummary {
		return rd
	}

	// who is on the case: the requester, the main assignee, then the people linked to it (active links only)
	deptOf := func(ids ...uuid.UUID) map[uuid.UUID]string {
		out := map[uuid.UUID]string{}
		var rows []struct {
			ID   uuid.UUID
			Name string
		}
		a.DB.Table("users u").Select("u.id AS id, d.name AS name").
			Joins("JOIN departments d ON d.id = u.department_id").
			Where("u.id IN ? AND u.organization_id = ?", ids, orgID).Scan(&rows)
		for _, r := range rows {
			out[r.ID] = r.Name
		}
		return out
	}
	ids := []uuid.UUID{occ.OpenedByUserID}
	if occ.AssignedUserID != nil {
		ids = append(ids, *occ.AssignedUserID)
	}
	var links []models.OccurrenceParticipant
	a.DB.Where("organization_id = ? AND occurrence_id = ?", orgID, occ.ID).Order("created_at ASC").Find(&links)
	for _, l := range links {
		ids = append(ids, l.UserID)
	}
	nm := a.chatUserNames(ids)
	dp := deptOf(ids...)
	rd.parties = append(rd.parties, reportParty{"Solicitante", nm[occ.OpenedByUserID], dp[occ.OpenedByUserID]})
	if occ.AssignedUserID != nil {
		rd.parties = append(rd.parties, reportParty{"Responsável principal", nm[*occ.AssignedUserID], dp[*occ.AssignedUserID]})
	}
	for _, v := range a.participantViews(links) {
		role := "Participante acompanhando"
		if v.Role == models.ParticipantRoleSupport {
			role = "Pessoa de apoio"
		}
		dep := v.DepartmentName
		if dep == "" {
			dep = dp[v.UserID]
		}
		rd.parties = append(rd.parties, reportParty{role, v.UserName, dep})
	}
	return rd
}

func humanBytes(n string) string {
	var v float64
	_, _ = fmt.Sscan(n, &v)
	switch {
	case v >= 1<<20:
		return fmt.Sprintf("%.1f MB", v/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.1f KB", v/(1<<10))
	}
	return fmt.Sprintf("%.0f B", v)
}

var reportPriorityLabels = map[string]string{"low": "Baixa", "normal": "Normal", "high": "Alta", "urgent": "Urgente"}

func (rd reportData) ago() string {
	d := rd.issuedAt.Sub(rd.occ.OpenedAt)
	switch {
	case d < time.Hour:
		return "há menos de 1 hora"
	case d < 48*time.Hour:
		return fmt.Sprintf("há %d h", int(d.Hours()))
	}
	return fmt.Sprintf("há %d dias", int(d.Hours()/24))
}

func (a *App) exportPDF(rd reportData, want map[string]bool, tables []exportTable, omitted []string, files []models.OccurrenceFile, held []string) []byte {
	occ := rd.occ
	stamp := rd.issuedAt.In(rd.loc).Format("02/01/2006 15:04")
	d := newPDF("Protocolo "+occ.ProtocolNumber+" - "+occ.Title, "Gerado em "+stamp+" por "+rd.issuedBy)
	d.org, d.ref = rd.org, rd.ref

	// identity block
	d.y -= 6
	d.textAt(repLeft, d.y-14, rd.org, 15, true, colInk)
	d.textRight(repLeft+repWidth, d.y-12, "RELATÓRIO DE OCORRÊNCIA", 9, true, colAccent)
	d.y -= 26
	d.hline(repLeft, d.y, repWidth, colLine)
	d.y -= 12
	d.textAt(repLeft, d.y-8, "CÓDIGO DO PROTOCOLO", 7, true, colMuted)
	d.textAt(repLeft, d.y-24, occ.ProtocolNumber, 17, true, colInk)
	d.y -= 34
	d.block(repLeft, repWidth, occ.Title, 11, false, colInk)
	d.y -= 2

	n := 0
	next := func() string { n++; return fmt.Sprintf("%02d", n) }

	if want["summary"] {
		d.section(next(), "Dados da ocorrência", "Situação, prazos e informações principais do atendimento")
		stage, state := "", label(exportStateLabels, string(occ.GeneralState))
		if occ.Stage != nil {
			stage = occ.Stage.Name
		}
		situation := strings.TrimSpace(stage)
		if state != "" && state != stage {
			situation += " (" + state + ")"
		}
		deadline := ""
		if occ.SLA.ResolutionDeadline != nil {
			deadline = tsvIn(rd.loc, *occ.SLA.ResolutionDeadline)
			if occ.SLA.Breached || (occ.ClosedAt == nil && occ.SLA.ResolutionDeadline.Before(rd.issuedAt)) {
				deadline += "  ATRASADO"
			}
		}
		customer := ""
		if occ.Contact != nil {
			customer = occ.Contact.ProfileName
		}
		unit, category := "", ""
		if occ.Unit != nil {
			unit = occ.Unit.Name
		}
		if occ.Category != nil {
			category = occ.Category.Name
		}
		closed := ""
		if occ.ClosedAt != nil {
			closed = tsvIn(rd.loc, *occ.ClosedAt)
		}
		d.grid([]reportCell{
			{"Situação", situation},
			{"Prioridade", label(reportPriorityLabels, string(occ.Priority))},
			{"Aberta em", tsvIn(rd.loc, occ.OpenedAt) + " (" + rd.ago() + ")"},
			{"Prazo para resolver", deadline},
			{"Nº do protocolo", occ.ProtocolNumber},
			{"Concluída em", closed},
			{"Cliente", customer},
			{"Pedido", occ.OrderRef},
			{"Unidade / loja", unit},
			{"Equipe", rd.team},
			{"Categoria / assunto", category},
		})
		if strings.TrimSpace(occ.Description) != "" {
			d.y -= 4
			d.textAt(repLeft+10, d.y-8, "DESCRIÇÃO", 7, true, colMuted)
			d.y -= 14
			d.block(repLeft+10, repWidth-20, occ.Description, 9.5, false, colInk)
		}

		d.section(next(), "Partes envolvidas", "Pessoas relacionadas à ocorrência e o papel de cada uma")
		rows := make([][]string, len(rd.parties))
		for i, p := range rd.parties {
			rows[i] = []string{p.role, p.name, p.department}
		}
		d.table([]reportCol{{"Papel", 150}, {"Colaborador(a)", 195}, {"Departamento", 170}}, rows)
	}

	titles := map[string]string{
		"historico":         "Histórico de interações",
		"discussao-interna": "Discussão interna",
		"whatsapp":          "Conversa WhatsApp",
		"documents":         "Documentos anexados",
	}
	subtitles := map[string]string{
		"historico":         "Registro cronológico das tratativas e mudanças de situação",
		"discussao-interna": "Mensagens trocadas pela equipe sobre esta ocorrência",
		"whatsapp":          "Mensagens trocadas com o cliente no canal da ocorrência",
		"documents":         "Arquivos registrados na ocorrência",
	}
	for _, t := range tables {
		d.section(next(), titles[t.name], fmt.Sprintf("%s  ·  %d registro(s)", subtitles[t.name], len(t.rows)))
		if len(t.rows) == 0 {
			d.block(repLeft+10, repWidth-20, "Nenhum registro.", 9.5, false, colMuted)
			continue
		}
		if t.name == "documents" {
			rows := make([][]string, len(t.rows))
			for i, r := range t.rows {
				rows[i] = []string{r[0], r[1], humanBytes(r[2]), r[3], r[4], r[5]}
			}
			d.table([]reportCol{{"Arquivo", 150}, {"Tipo", 80}, {"Tamanho", 55}, {"Enviado por", 90}, {"Data", 80}, {"Restrito", 60}}, rows)
			continue
		}
		for _, r := range t.rows {
			date, clock, _ := strings.Cut(r[0], " ")
			switch t.name {
			case "historico": // Data, Evento, Autor, Descrição
				d.timelineEntry(date, clock, r[2], r[1], r[3])
			case "discussao-interna": // Data, Autor, Mensagem, Anexos
				body := r[2]
				if r[3] != "" {
					body += "\n[anexos: " + r[3] + "]"
				}
				d.timelineEntry(date, clock, r[1], "", body)
			case "whatsapp": // Data, Sentido, Mensagem, Estado, Enviado por
				who := r[1]
				if r[4] != "" {
					who += " · " + r[4]
				}
				d.timelineEntry(date, clock, who, r[3], r[2])
			}
		}
	}

	if len(held) > 0 {
		d.section(next(), "Arquivos não incluídos", "")
		d.block(repLeft+10, repWidth-20, fmt.Sprintf("%d arquivo(s) não incluído(s) no ZIP porque ainda não receberam verificação antimalware limpa (em verificação, com falha ou bloqueados): %s.", len(held), strings.Join(held, ", ")), 9.5, false, colInk)
	}
	if len(omitted) > 0 {
		d.section(next(), "Seções não incluídas", "")
		d.block(repLeft+10, repWidth-20, "Por falta de permissão, as seguintes seções não fazem parte deste relatório: "+strings.Join(omitted, ", ")+".", 9.5, false, colInk)
	}

	// issuance record: who asked for the report and when; the code is also in the audit log
	d.section(next(), "Registro de emissão", "")
	zone, _ := rd.issuedAt.In(rd.loc).Zone()
	d.grid([]reportCell{
		{"Emitido por", rd.issuedBy},
		{"Data / hora de emissão", rd.issuedAt.In(rd.loc).Format("02/01/2006 15:04:05") + " (" + zone + ")"},
		{"Protocolo", occ.ProtocolNumber},
		{"Código de referência", rd.ref},
	})
	d.y -= 2
	d.block(repLeft+10, repWidth-20,
		"Este relatório foi gerado pelo WhatC com os dados registrados no sistema até a data e hora de emissão. "+
			"O código de referência é gravado no registro de auditoria da organização no momento da emissão. "+
			"O documento não possui assinatura digital nem certificação de autenticidade.", 8.5, false, colMuted)
	return d.bytes()
}
