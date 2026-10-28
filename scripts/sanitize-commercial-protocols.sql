-- Moves protocols that were addressed to a commercial (Vendas) team to the team of the person who opened them,
-- keeping every event, file, note and participant, and recording the correction in the case history and the audit log.
-- Only cases whose opener belongs to exactly ONE non-commercial team are moved; the rest are listed for a decision.
-- It never creates a sales opportunity: the CRM allows one open opportunity per customer, so a case whose customer
-- already has one just points to it in the history. Review the SELECT at the end; run on the intended database only.
BEGIN;

UPDATE departments d SET commercial = true
WHERE d.deleted_at IS NULL AND d.commercial = false AND (d.name ILIKE 'vendas%' OR d.name ILIKE 'comercial%');

CREATE TEMP TABLE fix ON COMMIT DROP AS
SELECT o.id AS occ_id, o.organization_id, o.team_id AS old_team, o.opened_by_user_id AS opener,
       (SELECT MIN(tm.team_id::text)::uuid FROM team_members tm JOIN teams t ON t.id = tm.team_id
         LEFT JOIN departments dd ON dd.id = t.department_id
         WHERE tm.user_id = o.opened_by_user_id AND t.is_active AND COALESCE(dd.commercial, false) = false) AS new_team,
       (SELECT COUNT(DISTINCT tm.team_id) FROM team_members tm JOIN teams t ON t.id = tm.team_id
         LEFT JOIN departments dd ON dd.id = t.department_id
         WHERE tm.user_id = o.opened_by_user_id AND t.is_active AND COALESCE(dd.commercial, false) = false) AS n_teams,
       (SELECT so.id FROM sales_opportunities so WHERE so.contact_id = o.contact_id AND so.organization_id = o.organization_id
         AND so.status = 'aberta' AND so.deleted_at IS NULL LIMIT 1) AS opportunity_id
FROM occurrences o
JOIN teams ot ON ot.id = o.team_id
JOIN departments od ON od.id = ot.department_id AND od.commercial = true
WHERE o.deleted_at IS NULL;

UPDATE occurrences o SET team_id = f.new_team, assigned_user_id = f.opener, updated_at = now()
FROM fix f WHERE f.occ_id = o.id AND f.n_teams = 1;

INSERT INTO occurrence_events (id, created_at, updated_at, organization_id, occurrence_id, type, content, metadata, created_by_id)
SELECT gen_random_uuid(), now(), now(), f.organization_id, f.occ_id, 'scope_regularized',
       'Destino corrigido: protocolos não são destinados a equipes comerciais',
       jsonb_build_object('reason', 'commercial destination not allowed', 'from_team_id', f.old_team, 'team_id', f.new_team,
                          'sales_opportunity_id', f.opportunity_id),
       f.opener
FROM fix f WHERE f.n_teams = 1;

INSERT INTO audit_logs (id, organization_id, resource_type, resource_id, user_id, user_name, action, changes, created_at)
SELECT gen_random_uuid(), f.organization_id, 'occurrence', f.occ_id, f.opener, 'Saneamento: destino comercial', 'updated',
       jsonb_build_array(jsonb_build_object('field', 'team_id', 'old', f.old_team, 'new', f.new_team),
                         jsonb_build_object('field', 'sales_opportunity_id', 'old', null, 'new', f.opportunity_id)),
       now()
FROM fix f WHERE f.n_teams = 1;

SELECT o.protocol_number, f.n_teams AS candidate_teams, f.new_team IS NOT NULL AS moved_or_candidate, f.opportunity_id
FROM fix f JOIN occurrences o ON o.id = f.occ_id;
COMMIT;
