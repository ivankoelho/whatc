package database

import (
	"fmt"
	"strings"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// grantRule liga uma capacidade que o papel já tem à permissão que ele ganha.
type grantRule struct {
	fromResource string
	fromAction   string
	toResource   string
	toAction     string
}

// occurrenceGrants é a equivalência exata com a capacidade de hoje. Quem
// administra etapas neste momento é quem tem settings.general:write, e é só
// esse que recebe as permissões de funil — roles:write não entra, porque
// concedê-lo alargaria acesso numa migração que promete não alterar o de
// ninguém.
var occurrenceGrants = []grantRule{
	{models.ResourceChat, models.ActionRead, models.ResourceOccurrences, models.ActionRead},
	{models.ResourceChat, models.ActionWrite, models.ResourceOccurrences, models.ActionWrite},
	// As três de etapa, incluindo read: sem ela a guarda de rota do frontend
	// esconde a tela de configuração até de quem pode editar.
	//
	// NÃO CORRIGIR sem decidir antes: um papel com settings.general:write e
	// SEM nenhuma permissão de chat recebe estas três mas não
	// occurrences:read, então ele abre a tela de configuração de etapas e a
	// chamada que lista etapas devolve 403. É proposital — o backfill
	// concede por equivalência com a capacidade de hoje, e esse papel não
	// tinha nenhum acesso ao CRM no dia anterior; conceder-lhe
	// occurrences:read alargaria acesso numa migração que promete não
	// alterar o de ninguém.
	{models.ResourceSettingsGeneral, models.ActionWrite, models.ResourceOccurrenceStages, models.ActionRead},
	{models.ResourceSettingsGeneral, models.ActionWrite, models.ResourceOccurrenceStages, models.ActionWrite},
	{models.ResourceSettingsGeneral, models.ActionWrite, models.ResourceOccurrenceStages, models.ActionDelete},
}

// occurrencePermissionKeys são as permissões que este backfill distribui. A
// guarda de "já foi semeado" compara contra esta lista em vez do tamanho de
// occurrenceGrants, que tem entradas repetidas por origem.
var occurrencePermissionKeys = []string{
	models.ResourceOccurrences + ":" + models.ActionRead,
	models.ResourceOccurrences + ":" + models.ActionWrite,
	models.ResourceOccurrenceStages + ":" + models.ActionRead,
	models.ResourceOccurrenceStages + ":" + models.ActionWrite,
	models.ResourceOccurrenceStages + ":" + models.ActionDelete,
}

// BackfillOccurrencePermissions concede as permissões do CRM aos papéis que já
// têm a capacidade equivalente por chat e por configurações gerais.
//
// Existe porque FixSystemRolePermissions pula qualquer papel que já tenha
// permissões, para não desfazer customizações — o que significa que uma
// permissão nova jamais chega a uma instalação existente por aquele caminho.
//
// É PURAMENTE ADITIVO: nunca revoga nada. Essa é a propriedade que torna o
// rollback seguro, porque a versão anterior autoriza por chat:* e ele
// permanece intacto.
//
// Idempotência é pela forma do dado, como BackfillChatbotFlowGraph: uma
// organização que já tenha qualquer papel com qualquer permissão occurrences%
// é pulada inteira, então isto roda uma vez por organização. Essa guarda de
// "já migrada" vive inteira dentro do INSERT (ver nota no SQL abaixo), não
// como uma lista de ids calculada à parte — isso evita tanto a divergência
// entre a checagem e a gravação quanto o limite de 65535 parâmetros do
// Postgres por statement, que uma lista de ids materializada esbarraria
// perto de ~65 mil organizações.
func BackfillOccurrencePermissions(db *gorm.DB, lo logf.Logger) error {
	// As linhas de permissão são criadas por SeedPermissionsAndRoles. Se ainda
	// não existem, não há o que ligar e este boot não faz nada. Isso é
	// silencioso por padrão — sem este Warn, um boot que rodou antes do seed
	// fica indistinguível no log de um boot que migrou tudo, e o primeiro
	// sintoma vira 403 para todo mundo no CRM.
	//
	// A checagem compara contra occurrencePermissionKeys chave por chave (não
	// só a contagem): um sexto permission sob "occurrences" somado a um dos
	// cinco renomeado ainda bateria len()==5 e passaria a guarda com o
	// conjunto errado.
	var seededRows []string
	if err := db.Model(&models.Permission{}).
		Where("resource IN ?", []string{models.ResourceOccurrences, models.ResourceOccurrenceStages}).
		Pluck("resource || ':' || action", &seededRows).Error; err != nil {
		return fmt.Errorf("failed to count occurrence permissions: %w", err)
	}
	seeded := make(map[string]bool, len(seededRows))
	for _, k := range seededRows {
		seeded[k] = true
	}
	for _, key := range occurrencePermissionKeys {
		if !seeded[key] {
			lo.Warn("Occurrence permissions backfill: occurrence permissions not seeded yet, did nothing",
				"resources", []string{models.ResourceOccurrences, models.ResourceOccurrenceStages})
			return nil
		}
	}

	// Só para o log: quantas organizações ainda não têm nenhum papel com
	// permissão occurrences%. A checagem que realmente decide o que grava é
	// a NOT EXISTS correlacionada dentro do INSERT logo abaixo — esta conta
	// não precisa (e não deve) ser reusada como lista de parâmetros.
	//
	// Sem filtro de o.deleted_at aqui de propósito: o INSERT abaixo também
	// não junta organizations, então processa papéis vivos dentro de
	// organizações soft-deleted. Contar só as vivas aqui faria esta conta
	// divergir do que o INSERT realmente decide gravar.
	var pendingOrgs int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM organizations o
		WHERE NOT EXISTS (
			SELECT 1
			FROM custom_roles r
			JOIN role_permissions rp ON rp.custom_role_id = r.id
			JOIN permissions p ON p.id = rp.permission_id
			WHERE r.organization_id = o.id
			  AND r.deleted_at IS NULL
			  AND p.resource LIKE 'occurrences%'
		  )`).Scan(&pendingOrgs).Error; err != nil {
		return fmt.Errorf("failed to count organisations pending the occurrence backfill: %w", err)
	}
	if pendingOrgs == 0 {
		lo.Info("Occurrence permissions backfill: nothing pending, all organisations already migrated")
		return nil
	}

	// Todas as regras de concessão viram UM único INSERT (a tabela g abaixo
	// é o occurrenceGrants em forma de VALUES), não um loop com um INSERT
	// por regra. Isso importa: dentro de um único statement o Postgres lê
	// role_permissions a partir de um snapshot tirado no início do
	// statement, então a guarda "organização ainda não migrada" não vê as
	// próprias linhas que este mesmo INSERT está gravando. Um loop de N
	// statements não teria essa propriedade — o primeiro INSERT (chat:read
	// -> occurrences:read) gravaria a permissão que o segundo INSERT
	// (chat:write -> occurrences:write) usa como sinal de "já migrada", e a
	// organização pareceria migrada a partir do segundo statement em
	// diante, perdendo as concessões seguintes no mesmo boot.
	placeholders := make([]string, len(occurrenceGrants))
	args := make([]any, 0, len(occurrenceGrants)*4)
	for i, g := range occurrenceGrants {
		placeholders[i] = "(?,?,?,?)"
		args = append(args, g.fromResource, g.fromAction, g.toResource, g.toAction)
	}

	query := fmt.Sprintf(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		JOIN (VALUES %s) AS g(from_resource, from_action, to_resource, to_action)
		  ON src.resource = g.from_resource AND src.action = g.from_action
		JOIN permissions target
		  ON target.resource = g.to_resource AND target.action = g.to_action
		WHERE r.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM role_permissions existing
			WHERE existing.custom_role_id = r.id
			  AND existing.permission_id = target.id
		  )
		  -- A guarda "organização ainda não migrada", correlacionada
		  -- diretamente em r.organization_id em vez de uma lista de ids
		  -- vinda de fora: um só lugar decide isso, não um SELECT separado
		  -- reexpandido como parâmetros do INSERT (o que esbarraria no
		  -- limite de 65535 parâmetros do Postgres por statement perto de
		  -- ~65 mil organizações).
		  AND NOT EXISTS (
			SELECT 1
			FROM custom_roles r2
			JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
			JOIN permissions p2 ON p2.id = rp2.permission_id
			WHERE r2.organization_id = r.organization_id
			  AND r2.deleted_at IS NULL
			  AND p2.resource LIKE 'occurrences%%'
		  )
		ON CONFLICT (custom_role_id, permission_id) DO NOTHING`,
		strings.Join(placeholders, ","),
	)

	res := db.Exec(query, args...)
	if res.Error != nil {
		return fmt.Errorf("failed to grant occurrence permissions: %w", res.Error)
	}

	lo.Info("Occurrence permissions backfill complete",
		"organisations_processed", pendingOrgs, "links_granted", res.RowsAffected)
	return nil
}

// helpdeskCatalogGrants concede administração do catálogo de Help Desk
// (unidades, departamentos, categorias e políticas de SLA de ocorrência) a
// quem já administra o funil de ocorrências. occurrences.stages:write é o
// sinal de equivalência: é exatamente a permissão que SystemRolePermissions
// já empacota junto com as onze daqui para o papel "manager" (ver
// internal/models/roles.go), então um papel que já administra etapas hoje é
// quem deveria administrar este catálogo também. Um "agent" nunca tem
// occurrences.stages:write, então nunca recebe nada daqui — coerente com
// SystemRolePermissions, que não dá units/departments/categories/sla_policies
// a agentes.
var helpdeskCatalogGrants = []grantRule{
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceUnits, models.ActionRead},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceUnits, models.ActionWrite},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceUnits, models.ActionDelete},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceDepartments, models.ActionRead},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceDepartments, models.ActionWrite},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceDepartments, models.ActionDelete},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceOccurrenceCategories, models.ActionRead},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceOccurrenceCategories, models.ActionWrite},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceOccurrenceCategories, models.ActionDelete},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceOccurrenceSLAPolicies, models.ActionRead},
	{models.ResourceOccurrenceStages, models.ActionWrite, models.ResourceOccurrenceSLAPolicies, models.ActionWrite},
}

// helpdeskCatalogPermissionKeys são as onze permissões que este backfill
// distribui. A guarda de "já foi semeado" compara contra esta lista em vez do
// tamanho de helpdeskCatalogGrants, que tem uma única origem repetida onze
// vezes.
var helpdeskCatalogPermissionKeys = []string{
	models.ResourceUnits + ":" + models.ActionRead,
	models.ResourceUnits + ":" + models.ActionWrite,
	models.ResourceUnits + ":" + models.ActionDelete,
	models.ResourceDepartments + ":" + models.ActionRead,
	models.ResourceDepartments + ":" + models.ActionWrite,
	models.ResourceDepartments + ":" + models.ActionDelete,
	models.ResourceOccurrenceCategories + ":" + models.ActionRead,
	models.ResourceOccurrenceCategories + ":" + models.ActionWrite,
	models.ResourceOccurrenceCategories + ":" + models.ActionDelete,
	models.ResourceOccurrenceSLAPolicies + ":" + models.ActionRead,
	models.ResourceOccurrenceSLAPolicies + ":" + models.ActionWrite,
}

// helpdeskCatalogMigratedClause is the NOT EXISTS reused both for the
// diagnostic pending-orgs count and the real guard inside the INSERT: an
// organisation counts as migrated once some live role of theirs already has
// any permission under one of this catalog's four resources. Four LIKEs
// because "occurrences.categories" and "occurrences.sla_policies" are two
// distinct sub-resources of "occurrences.", not a shared prefix between them.
//
// orgColumn names the column that identifies "this organisation" in the
// enclosing query ("o.id" for the plain count, "r.organization_id" inside the
// INSERT) — the only thing that differs between the two call sites.
//
// percent is the LIKE wildcard: a literal "%" for db.Raw (sent to Postgres
// as-is) but "%%" when the caller is about to run the result through
// fmt.Sprintf, which would otherwise consume a lone "%" as a format verb and
// corrupt the SQL. Sharing one clause between those two contexts without this
// parameter is exactly the kind of subtle bug this comment is here to head
// off — sending literal "%%" to Postgres degrades every guard into "resource
// LIKE '...%%'", which never matches anything and silently disables
// idempotency, re-granting on every boot without ever tripping ON CONFLICT.
func helpdeskCatalogMigratedClause(orgColumn, percent string) string {
	return fmt.Sprintf(`
	SELECT 1
	FROM custom_roles r2
	JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
	JOIN permissions p2 ON p2.id = rp2.permission_id
	WHERE r2.organization_id = %s
	  AND r2.deleted_at IS NULL
	  AND (
	        p2.resource LIKE 'units%s'
	     OR p2.resource LIKE 'departments%s'
	     OR p2.resource LIKE 'occurrences.categories%s'
	     OR p2.resource LIKE 'occurrences.sla_policies%s'
	  )`, orgColumn, percent, percent, percent, percent)
}

// BackfillHelpdeskCatalogPermissions concede as onze permissões novas da Fase
// 3 (unidades, departamentos, categorias e políticas de SLA de ocorrência)
// aos papéis que já administram o funil de ocorrências.
//
// Existe pela mesma razão que BackfillOccurrencePermissions:
// FixSystemRolePermissions pula qualquer papel que já tenha permissões, então
// uma organização cujo papel "manager" já tinha permissões antes desta Fase
// nunca ganha as onze novas por aquele caminho — elas ficam inacessíveis
// exceto por SQL direto.
//
// É PURAMENTE ADITIVO como o outro backfill: nunca revoga nada. Idempotência
// também é por organização: uma organização que já tenha qualquer papel com
// qualquer permissão units%/departments%/occurrences.categories%/
// occurrences.sla_policies% é pulada inteira.
func BackfillHelpdeskCatalogPermissions(db *gorm.DB, lo logf.Logger) error {
	var seededRows []string
	if err := db.Model(&models.Permission{}).
		Where("resource IN ?", []string{
			models.ResourceUnits, models.ResourceDepartments,
			models.ResourceOccurrenceCategories, models.ResourceOccurrenceSLAPolicies,
		}).
		Pluck("resource || ':' || action", &seededRows).Error; err != nil {
		return fmt.Errorf("failed to count helpdesk catalog permissions: %w", err)
	}
	seeded := make(map[string]bool, len(seededRows))
	for _, k := range seededRows {
		seeded[k] = true
	}
	for _, key := range helpdeskCatalogPermissionKeys {
		if !seeded[key] {
			lo.Warn("Helpdesk catalog permissions backfill: permissions not seeded yet, did nothing")
			return nil
		}
	}

	var pendingOrgs int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM organizations o
		WHERE NOT EXISTS (` + helpdeskCatalogMigratedClause("o.id", "%") + `)`).Scan(&pendingOrgs).Error; err != nil {
		return fmt.Errorf("failed to count organisations pending the helpdesk catalog backfill: %w", err)
	}
	if pendingOrgs == 0 {
		lo.Info("Helpdesk catalog permissions backfill: nothing pending, all organisations already migrated")
		return nil
	}

	// Um único INSERT, pela mesma razão documentada em
	// BackfillOccurrencePermissions: dentro de um statement só o Postgres lê
	// um snapshot fixo, então a guarda "organização ainda não migrada" não vê
	// as próprias linhas que este INSERT está gravando.
	placeholders := make([]string, len(helpdeskCatalogGrants))
	args := make([]any, 0, len(helpdeskCatalogGrants)*4)
	for i, g := range helpdeskCatalogGrants {
		placeholders[i] = "(?,?,?,?)"
		args = append(args, g.fromResource, g.fromAction, g.toResource, g.toAction)
	}

	query := fmt.Sprintf(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		JOIN (VALUES %s) AS g(from_resource, from_action, to_resource, to_action)
		  ON src.resource = g.from_resource AND src.action = g.from_action
		JOIN permissions target
		  ON target.resource = g.to_resource AND target.action = g.to_action
		WHERE r.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM role_permissions existing
			WHERE existing.custom_role_id = r.id
			  AND existing.permission_id = target.id
		  )
		  AND NOT EXISTS (`+helpdeskCatalogMigratedClause("r.organization_id", "%%")+`)
		ON CONFLICT (custom_role_id, permission_id) DO NOTHING`,
		strings.Join(placeholders, ","),
	)

	res := db.Exec(query, args...)
	if res.Error != nil {
		return fmt.Errorf("failed to grant helpdesk catalog permissions: %w", res.Error)
	}

	lo.Info("Helpdesk catalog permissions backfill complete",
		"organisations_processed", pendingOrgs, "links_granted", res.RowsAffected)
	return nil
}

// BackfillWhatHappenedPermission concede occurrences.what_happened:{read,write,delete}
// aos papéis que já administram occurrences.categories — a permissão irmã
// mais próxima, já que as duas telas de catálogo (Categorias e "O que
// aconteceu") nascem juntas e são gerenciadas pelas mesmas pessoas.
//
// Existe como backfill PRÓPRIO, não como uma quinta entrada em
// helpdeskCatalogGrants/helpdeskCatalogMigratedClause: aquela guarda de
// idempotência considera uma organização "migrada" assim que QUALQUER uma
// das quatro permissões antigas já existir — uma organização que já tinha
// occurrences.categories:read antes desta mudança seria marcada "já
// migrada" e nunca receberia occurrences.what_happened, exatamente o gap
// que a lição da Fase 3 (ver memória do projeto) pede para evitar.
//
// Puramente aditivo: nunca revoga nada.
func BackfillWhatHappenedPermission(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).
		Where("resource = ?", models.ResourceOccurrenceWhatHappened).
		Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to count the what-happened permission: %w", err)
	}
	if seeded == 0 {
		lo.Warn("occurrences.what_happened permissions not seeded yet, did nothing")
		return nil
	}

	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT DISTINCT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		JOIN permissions target ON target.resource = ? AND target.action = src.action
		WHERE r.deleted_at IS NULL
		  AND src.resource = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM role_permissions existing
		    WHERE existing.custom_role_id = r.id AND existing.permission_id = target.id
		  )
		ON CONFLICT DO NOTHING`,
		models.ResourceOccurrenceWhatHappened, models.ResourceOccurrenceCategories,
	)
	if res.Error != nil {
		return fmt.Errorf("failed to grant the what-happened permission: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		lo.Info("what-happened permission backfill: nothing pending")
		return nil
	}
	lo.Info("what-happened permission backfill complete", "links_granted", res.RowsAffected)
	return nil
}

// BackfillOccurrenceProcessesPermission concede occurrences.processes:{read,write,delete}
// aos papéis que já administram occurrences.categories — mesmo raciocínio de
// BackfillWhatHappenedPermission: telas de catálogo do SAC nascem juntas e são
// geridas pelas mesmas pessoas.
//
// Backfill PRÓPRIO (não entra em nenhum grupo existente) pela mesma razão
// documentada em BackfillWhatHappenedPermission: qualquer guarda de
// idempotência compartilhada marcaria como "já migrada" uma organização que
// só tinha as permissões antigas, e ela nunca receberia esta nova. Puramente
// aditivo: nunca revoga nada.
func BackfillOccurrenceProcessesPermission(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).
		Where("resource = ?", models.ResourceOccurrenceProcesses).
		Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to count the occurrence-processes permission: %w", err)
	}
	if seeded == 0 {
		lo.Warn("occurrences.processes permissions not seeded yet, did nothing")
		return nil
	}

	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT DISTINCT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		JOIN permissions target ON target.resource = ? AND target.action = src.action
		WHERE r.deleted_at IS NULL
		  AND src.resource = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM role_permissions existing
		    WHERE existing.custom_role_id = r.id AND existing.permission_id = target.id
		  )
		ON CONFLICT DO NOTHING`,
		models.ResourceOccurrenceProcesses, models.ResourceOccurrenceCategories,
	)
	if res.Error != nil {
		return fmt.Errorf("failed to grant the occurrence-processes permission: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		lo.Info("occurrence-processes permission backfill: nothing pending")
		return nil
	}
	lo.Info("occurrence-processes permission backfill complete", "links_granted", res.RowsAffected)
	return nil
}

// BackfillContactNamePermission concede contacts.name:write aos papéis que
// já renomeiam contatos hoje e aos que atendem conversas.
//
// ATENÇÃO — esta regra difere DE PROPÓSITO da de BackfillOccurrencePermissions.
// Lá a regra era equivalência exata: ninguém ganhava capacidade nova. Aqui a
// segunda origem, chat:write, é uma AMPLIAÇÃO deliberada — o produto pediu que
// quem atende possa corrigir o nome do contato sem depender de um gestor.
//
// Puramente aditivo, como o outro: nunca revoga nada, então um rollback
// continua funcionando com as permissões antigas intactas.
func BackfillContactNamePermission(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).
		Where("resource = ? AND action = ?", models.ResourceContactName, models.ActionWrite).
		Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to count the contact name permission: %w", err)
	}
	if seeded == 0 {
		lo.Warn("contacts.name permission not seeded yet, did nothing")
		return nil
	}

	// SELECT DISTINCT é cinto e suspensório, não necessidade: ON CONFLICT DO
	// NOTHING já deduplica linhas repetidas dentro da mesma sentença, então um
	// papel com as duas origens (contacts:write e chat:write) já backfillaria
	// limpo mesmo sem o DISTINCT. O que exigiria o DISTINCT de verdade é uma
	// troca futura para ON CONFLICT DO UPDATE — essa forma sim levanta "cannot
	// affect row a second time" diante de duplicidade na mesma sentença.
	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT DISTINCT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		CROSS JOIN permissions target
		WHERE r.deleted_at IS NULL
		  AND target.resource = ? AND target.action = ?
		  AND (
		    (src.resource = ? AND src.action = ?)
		    OR (src.resource = ? AND src.action = ?)
		  )
		  AND NOT EXISTS (
		    SELECT 1
		    FROM custom_roles r2
		    JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
		    JOIN permissions p2 ON p2.id = rp2.permission_id
		    WHERE r2.organization_id = r.organization_id
		      AND r2.deleted_at IS NULL
		      AND p2.resource = ?
		  )
		ON CONFLICT DO NOTHING`,
		models.ResourceContactName, models.ActionWrite,
		models.ResourceContacts, models.ActionWrite,
		models.ResourceChat, models.ActionWrite,
		models.ResourceContactName,
	)
	if res.Error != nil {
		return fmt.Errorf("failed to grant the contact name permission: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		lo.Info("contact name backfill: nothing pending")
		return nil
	}
	lo.Info("contact name backfill complete", "links_granted", res.RowsAffected)
	return nil
}
