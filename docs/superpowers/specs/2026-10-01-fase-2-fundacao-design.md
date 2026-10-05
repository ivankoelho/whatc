# Fase 2: fundação (tipo de contato, unidade/departamento, CPF/CNPJ)

## O que muda
- `contacts.contact_type` (`cliente` | `fornecedor` | `colaborador`), `NOT NULL DEFAULT 'cliente'` e
  `CHECK chk_contacts_contact_type`. Linhas existentes e contatos criados sem tipo ficam `cliente`.
  Novo tipo = nova constante em `models/constants.go` + ajuste do CHECK em `database/postgres.go`.
- `contacts.unit_id`, `contacts.department_id`, `users.unit_id`, `users.department_id`: nullable, indexados,
  com FK para `units`/`departments`. Informativos: não alteram permissões nem roteamento.
- Todo `unit_id`/`department_id` recebido é validado contra a organização do chamador (`parseOrgPlacement`);
  id de outra organização responde 404. `""` limpa o campo, ausente não toca nele.
- Alterar unidade/departamento de um usuário exige `users:write`, mesmo no próprio perfil.
- Excluir unidade/departamento é recusado (409) enquanto um time vivo, contato ou usuário (inclusive
  soft-deleted, para que restaurar não traga uma referência pendente) ainda aponta para ele.

## CPF / CNPJ
- Regra única em `contactutil.NormalizeDocumento`: vazio permitido; 11 dígitos = CPF válido; 14 = CNPJ válido;
  outro tamanho = erro. Entrada com ou sem máscara, armazenado só com dígitos.
  Usada por criar/editar contato e pelo vínculo X2 (que continua exigindo documento).
- A coluna continua `cpfcnpj` (não renomear): `Updates()` com map usa o nome literal da coluna.
- **Não há UNIQUE**: a identidade do contato é o telefone e a mesma pessoa pode ter vários contatos.
- `GET /api/contacts?search=` também encontra por documento (3+ dígitos, com ou sem máscara) e aceita os
  filtros `contact_type`, `unit_id`, `department_id`.

## Permissões
Nenhuma nova: `contacts:write` para tipo/documento/unidade/departamento do contato, `users:write` para o usuário.
O select de unidades/departamentos usa `units:read`/`departments:read`; sem elas a lista vem vazia.

## Reversão
Colunas aditivas e nullable (exceto `contact_type`, com default). Voltar à versão anterior as ignora.
