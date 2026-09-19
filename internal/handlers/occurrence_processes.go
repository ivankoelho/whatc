package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

func intPtr(v int) *int { return &v }

// occurrenceProcessSeed is the shape used only while seeding — plain names
// for category/what-happened instead of IDs, resolved via
// findOrCreateCategory/findOrCreateWhatHappened at seed time.
type occurrenceProcessSeed struct {
	name, category, whatHappened, guidance, restrictions string
	evidence, required                                   []string
	responseMinutes, resolutionMinutes                   *int
	registrationMessage, documentsMessage                string
}

// defaultOccurrenceProcesses are the five real processes that open a SAC
// protocol, transcribed from the product owner's validated process map
// (nodes 1.2, 1.3, 2.1, 2.2, 2.3 of the HTML's `sacMsg`-tagged tree — see
// this plan's header for the source file). Guidance/Restrictions/messages
// are the real `conduta`/`pr`/`dont`/`sacMsg`/`msg` text, not invented.
//
// responseMinutes/resolutionMinutes are calendar minutes — the same
// semantics OccurrenceSLAPolicy's own four rows already use, not a
// business-hours calendar. A comment quoting "5 dias úteis" names the
// source text, not a literal business-day conversion claim.
//
// Category/WhatHappened names: two (Quantidade Divergente, Produto com
// Avaria) reuse the six existing seeded defaults exactly. The other three
// what-happened names and all five category assignments are THIS PLAN'S
// interpretation — the HTML groups nodes by delivery stage ("Pedido
// efetuado"/"Material recebido"), not by the Category/WhatHappened
// vocabulary this codebase already uses. This mapping is a business
// decision pending product-owner validation (see this file's seed-time
// warning log and this plan's final report task), not a settled fact.
var defaultOccurrenceProcesses = []occurrenceProcessSeed{
	{
		name:         "Devolução após 24h",
		category:     "Devolução com Estorno",
		whatHappened: "Devolução Após 24h", // new — no existing default fits
		guidance: "Registrar a solicitação e encaminhar para validação, sem prometer data de crédito.\n" +
			"Abrir a solicitação de devolução no sistema.\n" +
			"Encaminhar à supervisora geral de contas a receber para validação.\n" +
			"Emitir o recibo de crédito, ou lançar como devolução a ser tratada no financeiro.\n" +
			"Liberar o material de volta ao estoque.",
		restrictions:    "Cancelamento é direito seu.\nVocê pode cancelar quando quiser.\nA gente troca sem problema.",
		evidence:        []string{"Nota fiscal", "Documento do titular", "Comprovante de pagamento"},
		required:        []string{"invoice_number"},
		responseMinutes: intPtr(72 * 60), // "72h" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• documento do titular;\n• confirmação de que o material não foi retirado;\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 1 dia útil com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• devolução com emissão de recibo de crédito, válido por 90 dias;\n• devolução com estorno, conforme a forma de pagamento;\n• manutenção da compra, caso a solicitação esteja fora das condições;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
	{
		name:         "Material já separado ou em romaneio",
		category:     "Reagendamento de Entrega",
		whatHappened: "Alteração de Pedido em Separação", // new
		guidance: "Conferir o material, reprogramar e confirmar a nova data por escrito.\n" +
			"Conferir e devolver o material separado ao estoque.\n" +
			"Retirar o pedido do romaneio da data original e reprogramar.\n" +
			"Recalcular o frete pela nova quantidade e peso.\n" +
			"Emitir a NF de ajuste.\n" +
			"Confirmar a nova data com o cliente por escrito.",
		restrictions:    "Já está separado, não dá para mudar.\nA entrega continua na mesma data.",
		evidence:        []string{"Nota fiscal", "Documento do titular", "Conferência do material separado", "Romaneio da data agendada"},
		required:        []string{"invoice_number", "product_description"},
		responseMinutes: intPtr(8 * 60), // "até 1 dia útil" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• documento do titular;\n• indicação do produto desejado;\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 1 dia útil com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• alteração do pedido com emissão de NF de ajuste;\n• alteração com cobrança ou estorno da diferença de frete;\n• manutenção do pedido original, se o produto desejado não tiver disponibilidade;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
	{
		name:         "Divergência no ato do recebimento",
		category:     "Troca de Produto",
		whatHappened: "Quantidade Divergente", // existing default — exact match
		guidance: "Acionar operações no mesmo atendimento e retornar em 48 horas úteis.\n" +
			"Acionar imediatamente a assistente de operações para a apuração.\n" +
			"Conferir a NF contra o romaneio e contra o registro fotográfico da entrega.\n" +
			"Quantidade a menor: incluir no registro da entrega para averiguação.\n" +
			"Produto ou lote divergente: retornar todo o material e replanejar a entrega.",
		restrictions:    "A loja errou.\nO motorista errou.\nVocê que escolheu errado.",
		evidence:        []string{"Nota fiscal", "Foto do produto recebido", "Foto da etiqueta da caixa", "Foto de todos os volumes"},
		required:        []string{"invoice_number", "product_description"},
		responseMinutes: intPtr(48 * 60), // "48h úteis" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• fotos do produto recebido;\n• foto da etiqueta da caixa;\n• fotos de todos os volumes entregues;\n\n" +
			"Pedimos que preserve o material e as embalagens até a conclusão da análise.\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 48 horas úteis com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• reenvio do material correto, por nossa conta, quando a divergência for da entrega;\n• complementação da quantidade faltante;\n• abertura de troca, caso o cliente opte por outro produto;\n• manutenção da entrega, quando o material conferir com a nota fiscal;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
	{
		name:         "Avaria — comunicação e abertura",
		category:     "Troca de Produto",
		whatHappened: "Produto com Avaria", // existing default — exact match
		guidance: "Registrar sem classificar a causa e informar o número do ticket ao cliente.\n" +
			"Aplicar apenas a triagem de exclusão — o atendente NÃO classifica a causa.\n" +
			"Abrir o ticket e informar o número ao cliente.\n" +
			"Solicitar as fotos e a descrição.\n" +
			"Puxar as imagens da entrega registradas no app do motorista.",
		restrictions:    "Vamos trocar seu produto.\nRealmente veio com defeito.\nIsso foi no transporte.\nA fábrica que resolve.",
		evidence:        []string{"Fotos do produto", "Fotos da embalagem", "Nota fiscal", "Quantidade afetada em m² e caixas"},
		required:        []string{"invoice_number", "product_description"},
		responseMinutes: intPtr(5 * 24 * 60), // "retorno ao cliente: 5 dias úteis" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• fotos do produto;\n• fotos da embalagem;\n• quantidade de caixas afetadas;\n\n" +
			"Importante: pedimos que NÃO utilize o material e que NÃO retire as peças da embalagem até a conclusão da análise.\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 5 dias úteis com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• substituição do material avariado;\n• abatimento proporcional do preço;\n• restituição do valor pago;\n• conclusão de que não há avaria coberta, com apresentação das evidências;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
		documentsMessage: "Olá, Sr.(a) [Nome].\n\nLamentamos o ocorrido e agradecemos por nos informar.\n\n" +
			"Para darmos andamento, pedimos que nos encaminhe:\n• Nota Fiscal da compra;\n• fotos do produto;\n• fotos da embalagem;\n• a quantidade de caixas afetadas.\n\n" +
			"Importante: pedimos que NÃO utilize o material e que NÃO retire as peças da embalagem até a conclusão da análise. Isso é necessário para preservar a avaliação e as alternativas do seu caso.\n\n" +
			"Seu ticket será aberto e nossa equipe retornará em até 5 dias úteis com o encaminhamento.",
	},
	{
		name:         "Desistência sem avaria",
		category:     "Devolução com Estorno",
		whatHappened: "Desistência do Pedido", // new
		guidance: "Analisar as imagens antes de autorizar e explicar o prazo de devolução.\n" +
			"Aplicar a triagem de exclusão antes de abrir o protocolo.\n" +
			"Abrir o protocolo pelo WhatsApp da empresa.\n" +
			"Analisar as condições de recebimento cruzando as imagens do cliente com as fotos do app do motorista.\n" +
			"Autorizar ou recusar a devolução com base na análise de imagens.",
		restrictions:      "Você tem 7 dias de arrependimento.\nTrocamos qualquer produto.\nPode trazer que a gente resolve.",
		evidence:          []string{"Nota fiscal", "Fotos das caixas fechadas", "Registro do canal da venda"},
		required:          []string{"invoice_number", "sale_channel"},
		responseMinutes:   intPtr(2 * 24 * 60), // "análise em 2 dias úteis" in the source text — calendar minutes
		resolutionMinutes: intPtr(5 * 24 * 60), // "devolução em 5 dias" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• fotos das caixas fechadas;\n• foto da etiqueta de lote;\n\n" +
			"A análise compara as imagens enviadas com o registro fotográfico feito na entrega.\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 2 dias úteis com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• autorização da devolução, com entrega do material no ponto de saída em até 5 dias;\n• emissão de recibo de crédito após a conferência do material;\n• recusa da devolução, quando as condições do produto não permitirem;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
}

// findOrCreateWhatHappened looks up a reason by exact name within the org,
// creating it at the end of the list when absent.
func (a *App) findOrCreateWhatHappened(orgID uuid.UUID, name string) (*models.OccurrenceWhatHappened, error) {
	if err := a.ensureDefaultWhatHappened(orgID); err != nil {
		return nil, err
	}
	var existing models.OccurrenceWhatHappened
	err := a.DB.Where("organization_id = ? AND name = ?", orgID, name).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var maxPosition int
	if err := a.DB.Model(&models.OccurrenceWhatHappened{}).Where("organization_id = ?", orgID).
		Select("COALESCE(MAX(position), -1)").Scan(&maxPosition).Error; err != nil {
		return nil, err
	}

	row := models.OccurrenceWhatHappened{OrganizationID: orgID, Name: name, Position: maxPosition + 1, IsActive: true}
	if err := a.DB.Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// findOrCreateCategory mirrors findOrCreateWhatHappened for OccurrenceCategory.
func (a *App) findOrCreateCategory(orgID uuid.UUID, name string) (*models.OccurrenceCategory, error) {
	if err := a.ensureDefaultCategories(orgID); err != nil {
		return nil, err
	}
	var existing models.OccurrenceCategory
	err := a.DB.Where("organization_id = ? AND name = ?", orgID, name).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var maxPosition int
	if err := a.DB.Model(&models.OccurrenceCategory{}).Where("organization_id = ?", orgID).
		Select("COALESCE(MAX(position), -1)").Scan(&maxPosition).Error; err != nil {
		return nil, err
	}

	row := models.OccurrenceCategory{OrganizationID: orgID, Name: name, Position: maxPosition + 1, IsActive: true}
	if err := a.DB.Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ensureDefaultOccurrenceProcesses seeds the five real SAC-opening processes
// on first read, same count-then-insert idempotency as
// ensureDefaultWhatHappened. Logs a warning naming the Category/WhatHappened
// mapping as a pending business decision every time it actually seeds, so it
// stays visible in server logs rather than only in a code comment nobody
// reads before go-live.
func (a *App) ensureDefaultOccurrenceProcesses(orgID uuid.UUID) error {
	var count int64
	if err := a.DB.Model(&models.OccurrenceProcess{}).
		Where("organization_id = ?", orgID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	a.Log.Warn("Seeding default occurrence processes — Category/WhatHappened mapping and SLA minutes are a first-cut interpretation of the validated process map, pending product-owner validation",
		"organization_id", orgID)

	// Category/reason rows are idempotent by name, so resolve them before the
	// transaction rather than holding the advisory lock across those lookups.
	categoryIDs := make([]uuid.UUID, len(defaultOccurrenceProcesses))
	whatHappenedIDs := make([]uuid.UUID, len(defaultOccurrenceProcesses))
	for i, seed := range defaultOccurrenceProcesses {
		category, err := a.findOrCreateCategory(orgID, seed.category)
		if err != nil {
			return err
		}
		whatHappened, err := a.findOrCreateWhatHappened(orgID, seed.whatHappened)
		if err != nil {
			return err
		}
		categoryIDs[i], whatHappenedIDs[i] = category.ID, whatHappened.ID
	}

	// One transaction: any failure rolls the whole seed back (no partial org),
	// and the per-org advisory lock serializes concurrent first reads so the
	// loser sees the winner's rows and returns without inserting anything.
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "occurrence_processes_seed:"+orgID.String()).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.OccurrenceProcess{}).
			Where("organization_id = ?", orgID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}

		for i, seed := range defaultOccurrenceProcesses {
			required := make(models.JSONBArray, len(seed.required))
			for j, r := range seed.required {
				required[j] = r
			}
			evidence := make(models.JSONBArray, len(seed.evidence))
			for j, e := range seed.evidence {
				evidence[j] = e
			}

			process := models.OccurrenceProcess{
				OrganizationID:    orgID,
				Name:              seed.name,
				CategoryID:        &categoryIDs[i],
				WhatHappenedID:    &whatHappenedIDs[i],
				Guidance:          seed.guidance,
				Restrictions:      seed.restrictions,
				EvidenceChecklist: evidence,
				RequiredFields:    required,
				ResponseMinutes:   seed.responseMinutes,
				ResolutionMinutes: seed.resolutionMinutes,
				IsActive:          true,
				Position:          i,
			}
			if err := tx.Create(&process).Error; err != nil {
				return err
			}

			messages := []models.OccurrenceProcessMessage{
				{OrganizationID: orgID, ProcessID: process.ID, Stage: models.OccurrenceProcessMessageRegistration, Content: seed.registrationMessage, IsActive: true},
			}
			if seed.documentsMessage != "" {
				messages = append(messages, models.OccurrenceProcessMessage{
					OrganizationID: orgID, ProcessID: process.ID, Stage: models.OccurrenceProcessMessageDocuments, Content: seed.documentsMessage, IsActive: true,
				})
			}
			if err := tx.Create(&messages).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
