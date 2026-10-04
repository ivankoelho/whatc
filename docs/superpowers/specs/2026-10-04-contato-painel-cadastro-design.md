# Painel do contato: tipo, CPF/CNPJ e, só para colaborador, unidade e departamento

Nota de design. Estado: **aguardando aprovação**; nada implementado.

## 1. Objetivo e regra

Mostrar e editar, no painel lateral do contato da tela de conversa (`ContactInfoPanel.vue`), os dados cadastrais que hoje só existem em Configurações → Contatos → detalhe:

- **Tipo** (`cliente`, `fornecedor`, `colaborador`) e **CPF/CNPJ**: sempre visíveis.
- **Unidade** e **Departamento**: visíveis **somente quando o tipo é `colaborador`**. Para `cliente` (e `fornecedor`) esses campos não têm relação e não aparecem.

## 2. Estado atual (verificado no código)

- Backend completo desde a Fase 2: `contacts.contact_type`, `unit_id`, `department_id`, `cpfcnpj`; `GET /api/contacts` já devolve `contact_type`, `cpf_cnpj`, `unit_id`, `department_id`; `PUT /api/contacts/{id}` aceita os quatro (CPF/CNPJ validado por `contactutil.NormalizeDocumento`, unidade/departamento validados contra a organização).
- Frontend: os campos existem só em `views/settings/ContactDetailView.vue`, sempre os quatro, sem regra por tipo. O `ContactInfoPanel.vue` (onde o atendente trabalha) **não mostra nenhum deles**. O tipo `Contact` da store já tem os campos.
- A unidade e o departamento do contato **não são informativos puros**. Dois consumidores:
  1. **Conhecimento (RAG, Fase 8B-3):** `chatbot_knowledge.go` usa a unidade e o departamento do contato como escopo dos documentos que a IA pode ver para ele (`q.UnitID, q.DepartmentID = c.UnitID, c.DepartmentID`); sem eles, só o conteúdo global.
  2. **Campanhas segmentadas (Fase 5):** filtros `unit_id`/`department_id` sobre contatos.

## 3. Consequência da regra: o dado precisa acompanhar a tela

Esconder os campos na interface sem tratar o valor deixaria um contato `cliente` com unidade/departamento **invisíveis** que ainda alteram o conhecimento que a IA enxerga para ele e a segmentação. Por isso a regra vale também no servidor.

**Regra do servidor.** O tipo final do contato decide: se não for `colaborador`, `unit_id` e `department_id` ficam `NULL`.
- Criar contato: sem tipo ou tipo diferente de `colaborador` ⇒ unidade e departamento descartados.
- Editar contato: se o tipo resultante (o enviado ou, se não enviado, o atual) não é `colaborador`, os dois campos são zerados no mesmo `UPDATE`, inclusive quando só o tipo mudou ou quando o cliente envia unidade num contato que não é colaborador.
- O servidor limpa em silêncio (não responde 400): a interface já esconde os campos, e um cliente de API antigo que ainda envie unidade para um `cliente` não quebra.
- A validação contra a organização continua valendo para o que for gravado.

**Dados já existentes.** Esta fase **não faz migração**. Um `cliente` que já tenha unidade/departamento só é limpo na próxima edição dele. Para decidir uma limpeza em massa, a implementação inclui uma consulta somente de leitura (contagem de contatos não colaboradores com unidade ou departamento por organização), e qualquer `UPDATE` em massa fica como passo separado, com a sua autorização. A consulta (validada por teste, `TestLegacyPlacementCount_ByOrganization`):

```sql
SELECT organization_id, COUNT(*) AS contatos_com_vinculo_indevido
FROM contacts
WHERE deleted_at IS NULL
  AND contact_type <> 'colaborador'
  AND (unit_id IS NOT NULL OR department_id IS NOT NULL)
GROUP BY organization_id;
```

## 4. Interface

**`ContactInfoPanel.vue`**: nova seção "Cadastro", seguindo o padrão das seções do painel (recolhível, ao lado de tags, oportunidades e ocorrências).
- Leitura: tipo, CPF/CNPJ (formatado), e, se `colaborador`, unidade e departamento (nomes, não ids).
- Edição: botão "Editar" que só aparece com `contacts:write`; abre um formulário com Tipo, CPF/CNPJ e, **enquanto o tipo selecionado for colaborador**, Unidade e Departamento. Trocar o tipo no formulário mostra ou esconde os dois campos na hora, e ao salvar de um tipo não colaborador envia os dois como `""` (limpar). Salvar chama `PUT /api/contacts/{id}` só com os campos alterados; erros do servidor (CPF/CNPJ inválido) aparecem no formulário.
- Nomes de unidade e departamento: listas lidas uma vez (`units:read`/`departments:read`). Sem essas permissões o select fica vazio e a leitura mostra "definido" sem o nome, sem erro (mesmo comportamento da tela de configurações).
- Ao salvar, a store do contato é atualizada (como já ocorre com nome e tags) para a lista de conversas e o painel não ficarem defasados.

**`ContactDetailView.vue`** (Configurações): aplica a mesma regra de exibição, para as duas telas não divergirem.

A regra de exibição vive num único helper (`showsPlacement(type)`) usado pelas duas telas.

Textos novos em `pt-BR` e `en` (`contacts.*`), reaproveitando as chaves existentes (`contacts.type`, `contacts.document`, `contacts.unit`, `contacts.department`, `contacts.noneSelected`).

## 5. Fora de escopo

- Migração em massa dos dados existentes (item 3).
- Qualquer mudança em roteamento, permissões, visibilidade, SLA ou na lógica do RAG e das campanhas (só deixam de receber unidade/departamento de quem não é colaborador).
- Integração com Nextcloud e canais internos.
- Colunas ou filtros na lista de contatos (não verificado se existem; ficam como estão).

## 6. Testes

**Backend (`internal/handlers`)**
- Criar `colaborador` com unidade e departamento: gravados. Criar `cliente` (e `fornecedor`) com unidade: descartada.
- Editar trocando `colaborador` → `cliente`: unidade e departamento viram `NULL`.
- Editar só a unidade de um `cliente`: ignorada. Editar a unidade de um `colaborador`: gravada.
- Unidade de outra organização continua dando 404 para colaborador.
- RAG: um contato `cliente` com unidade antiga e depois editado deixa de receber escopo de unidade (só global).
- Consulta de contagem de legados: só leitura.

**Frontend (vitest)**
- `showsPlacement`: verdadeiro só para `colaborador`.
- Seção do painel: leitura por tipo, formulário mostra/esconde unidade e departamento ao trocar o tipo, payload com `""` ao sair de colaborador, botão "Editar" ausente sem `contacts:write`.

## 7. Plano (depois da aprovação)

1. Backend: regra do servidor + testes.
2. Frontend: helper, seção do painel, ajuste de `ContactDetailView`, i18n + testes.
3. Verificação visual no navegador (painel e configurações) e suíte local completa.

Branch `feature/contact-panel-registration-fields`, a partir de `development` (`4e2b75f`). Sem push antes da sua revisão local.

## 8. Pontos para decidir

1. **Servidor limpa unidade/departamento quando o tipo não é colaborador** (recomendado) ou só a interface esconde? Só esconder deixa valores invisíveis afetando RAG e campanhas.
2. **Dados existentes:** sem migração agora, com a consulta de contagem para decidir depois. De acordo?
3. **`ContactDetailView` também segue a regra** (recomendado) ou só o painel?
4. **Fornecedor** também fica sem unidade e departamento (assumido, pela sua regra de "somente colaborador")?
5. **Edição no painel** com `contacts:write`, no mesmo padrão de tags, ou só leitura no painel e edição continua em Configurações?

## 9. Garantias que não mudam

`ai_tools.providers = []` e `write_enabled = false`. Nenhuma tabela nova nem coluna nova: só comportamento da API de contatos e telas. Merge só com aprovação explícita, sem squash nem rebase.
