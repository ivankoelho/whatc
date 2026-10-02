# Fase 8A: Knowledge Core (documentos, chunks, busca lexical, escopo, citações)

Status: **nota de design para revisão. Nada implementado.**

## Escopo

8A entrega o domínio de conhecimento e uma API real de busca, **sem LLM, sem embeddings e sem ligação ao chatbot**.
8B (depois): tela de gestão, upload, reindexação, integração com o chatbot atrás de `knowledge.rag_enabled=false`,
montagem de contexto com citações, preparação do assistente do agente.

Fora da 8A: PDF/OCR, embeddings, `pgvector`, versionamento, `knowledge:delete|admin|publish`, migração de `AIContext`
(que continua existindo e coexiste: contexto operacional por palavra-chave, diferente de conteúdo documental recuperado).

## Decisões fechadas

| Ponto | Decisão |
|---|---|
| Armazenamento | PostgreSQL FTS (`tsvector`, config `portuguese`), sem extensão nova |
| Contrato | interface `Retriever` desde o início; FTS é a primeira implementação, não um provisório |
| Acesso | organização → unidade → departamento + visibilidade; filtro **dentro da consulta SQL** |
| Fontes | FAQ, artigo, processo/procedimento, texto, Markdown, importação dos HTML de `manuais/` |
| Permissões | `knowledge:read`, `knowledge:write` (nenhuma outra) |
| Independência | Knowledge não importa `internal/ai` nem `AIContext`; funciona sem provedor de IA |

## Modelo

`KnowledgeDocument` (`knowledge_documents`)
- `organization_id` (not null), `unit_id` (null), `department_id` (null)
- `visibility`: `organization | unit | department` (ver regras abaixo; derivada e validada, não livre)
- `source_type`: `faq | article | process | text | markdown | manual_html`
- `title`, `origin` (ex.: `manual/Central-Whatc-Guia-do-Agente#s-fila`, `manual:<slug>`), `body` (texto normalizado),
  `content_hash` (sha256 do corpo; reimportação idêntica não regrava), `status` (`active|archived`),
  `created_by_id`, `updated_by_id`.
- Exclusão lógica (`BaseModel`); arquivar tira o documento da busca sem apagar.

`KnowledgeChunk` (`knowledge_chunks`)
- `document_id`, **`organization_id`, `unit_id`, `department_id`, `visibility` e `status` denormalizados do documento**
  (para o filtro de escopo ser um `WHERE` simples no próprio chunk, com índice, sem JOIN obrigatório);
  atualizados na mesma transação quando o documento muda de escopo/status.
- `chunk_index`, `heading` (trilha de títulos), `content`, `metadata` (jsonb: âncora, posição, fonte),
  `search_vector tsvector` gerado: `setweight(heading,'A') || setweight(content,'B')`, config `portuguese`.
- Índice GIN em `search_vector`; índice `(organization_id, status, visibility)`; `(document_id, chunk_index)` único.

Migração: AutoMigrate + SQL idempotente (padrão do projeto) para a coluna gerada/trigger do `tsvector` e o GIN.
Aditiva; nada existente é alterado.

## Regras de escopo

Um documento é definido por (`unit_id`, `department_id`):
- ambos nulos → global da organização;
- `unit_id` → unidade; `department_id` → departamento (com ou sem unidade, conforme o cadastro do projeto: departamentos
  hoje são da organização; `unit_id` e `department_id` juntos = "departamento naquela unidade").

O consultante informa um **contexto**: `unit_id?`, `department_id?` (a API de busca os deriva do usuário/conversa; ver
abaixo). Um chunk é elegível se:

```
organization_id = :org
AND status = 'active'
AND (unit_id IS NULL       OR unit_id       = :unit)
AND (department_id IS NULL OR department_id = :dept)
```

Contexto sem unidade/departamento enxerga só o global. Documento de unidade nunca aparece sem a unidade
correspondente; documento de departamento exige o departamento. O filtro está **na cláusula WHERE da consulta FTS**,
antes do ranking e do `LIMIT`: nunca recupera tudo para filtrar em código.

Na API de busca para humanos (8A): `knowledge:read` + parâmetros opcionais `unit_id`/`department_id`.
O servidor **restringe** o contexto ao que o usuário pode ter (unidades/departamentos aos quais pertence, ou todos se a
função enxerga tudo, seguindo a regra de visibilidade já usada em conversas/ocorrências; confirmar o helper existente
na implementação). Parâmetro fora do alcance do usuário = 403, nunca ignorado em silêncio.

Escrita (`knowledge:write`): o escopo do documento é validado contra a organização (unidade/departamento de outra
organização = 404/400); mesma regra de IDs com escopo de organização da Fase 2.

## Retriever

```go
type Query struct {
    OrgID uuid.UUID
    Text  string
    UnitID, DepartmentID *uuid.UUID // contexto do consultante
    Limit int
}
type Hit struct {
    DocumentID, ChunkID uuid.UUID
    Title, Heading, Content string
    Score float64          // relevância lexical, não probabilidade
    Citation Citation      // título, source_type, origin, âncora
}
type Retriever interface { Retrieve(ctx context.Context, q Query) ([]Hit, error) }
```

- `LexicalRetriever` (8A): `websearch_to_tsquery('portuguese', :q)` + `ts_rank_cd`, filtro de escopo no WHERE,
  `LIMIT` máx. (padrão 5, teto 20), `ts_headline` opcional para trecho. Consulta vazia/só stop-words = lista vazia.
- Futuro: `VectorRetriever`, `HybridRetriever` implementam a mesma interface; a camada de negócio (`KnowledgeService`)
  e a API não mudam.
- `KnowledgeService` recebe o `Retriever` por injeção (teste com fake; sem LLM).

## Ingestão

Pipeline único: `fonte → normalização → chunking → grava documento+chunks (uma transação)`.
- **Normalização:** UTF-8, quebras de linha, espaços repetidos, remoção de caracteres de controle; HTML vira texto
  (ver abaixo); Markdown mantém títulos como `heading` e remove só a marcação.
- **Chunking:** por seção/título; seção longa é dividida por parágrafo com teto (~1.000 caracteres) e pequena
  sobreposição (~100); sem cortar no meio de palavra; `chunk_index` estável. Constantes no código (sem config).
- **Reimportação:** por `origin`; mesmo `content_hash` = nada a fazer; hash diferente = substitui os chunks do
  documento na mesma transação (nunca deixa o documento sem chunks no meio).
- **Texto/Markdown/FAQ/artigo/processo:** `POST/PUT/DELETE /api/knowledge/documents` (`knowledge:write`).
  FAQ guarda pergunta no título e resposta no corpo. Sem upload de arquivo na 8A (8B).
- **Manuais HTML:** importação **controlada e explícita**, não leitura em tempo de consulta. **Somente CLI** na 8A
  (`whatomate knowledge import-manuals -org <id> [-dir manuais]`, no padrão do subcomando `-migrate`); **não há endpoint
  de importação** (a superfície administrativa vem com a tela, na 8B); escopo global da organização por padrão (`--unit/--department` opcionais). O parser usa
  `golang.org/x/net/html` (já no módulo se existir; senão confirmar antes de adicionar dependência):
  descarta `script`/`style`/`img` (os HTML têm ~0,25 MB e ~1,5 MB de imagens base64 embutidas, que **nunca** entram no
  texto), divide por `h2`/`h3` com a âncora (`id`) como parte da citação, texto das seções vira documento
  `manual_html` com `origin = manual/<arquivo>#<âncora>`.

## API (8A)

- `GET /api/knowledge/search?q=&unit_id=&department_id=&limit=` (`knowledge:read`) → `{results:[{document_id, chunk_id,
  title, heading, content, score, citation:{title, source_type, origin, anchor}}]}`. `score` documentado como relevância
  lexical relativa à consulta (comparável só dentro da mesma resposta).
- `GET /api/knowledge/documents`, `GET /…/{id}` (`knowledge:read`, respeitando o escopo do consultante),
  `POST`, `PUT /…/{id}`, `DELETE /…/{id}` (`knowledge:write`; DELETE é exclusão lógica).
- Cada escrita é auditada (`audit.LogAudit`, lembrando: compara só chaves do estado novo; remoção/arquivamento
  precisa de mudança explícita, como aprendido na 3B).

## Permissões

`knowledge:read`, `knowledge:write`: recurso `knowledge` em `models/roles.go`, backfill idempotente em
`permissions_backfill.go` (**o backfill concede ambas somente ao papel de sistema `admin`**; nenhum outro papel, inclusive gestor, recebe
permissão Knowledge na 8A; elas continuam atribuíveis depois sem mudar o domínio).

Alcance do consultante: quem tem `knowledge:write` ou `conversations:view_all` tem alcance global e pode informar
`unit_id`/`department_id`; os demais usam o `unit_id`/`department_id` do próprio cadastro e um parâmetro diferente
disso é 403. Contexto sem unidade/departamento vê só o global.

Acentos: a normalização (minúsculas, sem acento) vale **só para o campo de busca** (`search_text`, indexado) e para a
consulta, com a mesma função; `title`, `heading` e `content` ficam originais para exibição e citação. Sem `unaccent`.

## Testes (8A)

- Chunking: seção curta/longa, sobreposição, sem cortar palavra, determinismo, Markdown com títulos.
- Normalização/HTML dos manuais: remove script/style/img/base64, mantém texto e âncora (fixture pequena + importação
  real dos dois arquivos de `manuais/` com contagem de documentos).
- FTS em português: acentuação (`orçamento` x `orcamento`?), flexão (`ocorrência/ocorrências`), ranking (título pesa mais
  que corpo), consulta vazia/stop-words, `websearch` com aspas/menos.
- **Escopo:** global visível a todos; unidade só com a unidade; departamento só com o departamento; contexto sem
  unidade não vê documento de unidade; outra organização nunca aparece; arquivado e excluído não aparecem; mudança de
  escopo/status do documento reflete nos chunks (denormalização); o filtro está no SQL (teste com `LIMIT 1` em que o
  melhor chunk fora do escopo não "consome" o limite).
- API: permissões (`knowledge:read` vs `write`, usuário sem nenhuma), parâmetros fora do alcance = 403, auditoria
  (criar, editar, arquivar/excluir), reimportação idempotente por hash, citação preenchida.
- Interface `Retriever` testada com fake no `KnowledgeService` (garante que a camada não depende do FTS).
- Suíte completa: só as 3 falhas ambientais conhecidas.

## Riscos e limites conhecidos

- FTS é lexical: sinônimos e perguntas parafraseadas não casam. Mitigação: contrato `Retriever`; embeddings depois.
- `portuguese` do Postgres faz stemming mas não remove acento: decidido normalizar no código (`FoldForSearch`), na
  indexação e na consulta, sem `unaccent`. O `tsvector` é uma coluna gerada a partir das cópias normalizadas
  (`search_heading`, `search_text`); `title`/`heading`/`content` ficam originais.
- Denormalização de escopo nos chunks exige atualização transacional; coberta por teste.
- Nada nesta fase altera `AIContext`, chatbot, discovery/X2 ou configuração.

## Semântica do PUT e alcance global (como ficou implementado)

- **PUT = representação completa** de título, corpo e escopo; **não existe PATCH na 8A**. `unit_id` e `department_id`
  são **obrigatórios** no PUT, cada um um UUID ou `null` explícito (= sem restrição). Omitir qualquer um é 400: um corpo
  parcial nunca vira "global" por acidente. Um UUID malformado é 400, nunca `null`. No POST, chave ausente = `null`.
  A tela da 8B deve enviar sempre as duas chaves.
- **Alcance global** é decidido num único helper (`hasGlobalKnowledgeReach`): `knowledge:write` ou
  `conversations:view_all`. Consequência do modelo atual (só o admin tem `knowledge:*`): conceder `knowledge:write` a um
  papel também lhe dá alcance sobre toda a base da organização. Se a 8B precisar de escrita limitada à unidade/
  departamento do autor, a regra é refinada nesse helper, sem espalhar `write || view_all` pelo código.

## Verificação com o app real (smoke)

Banco descartável e Redis isolado, app iniciado com `-migrate`: 22 chamadas HTTP (login, CRUD, busca, 403/401, PUT sem
escopo, arquivar, excluir) e a CLI `knowledge import-manuals` (45 documentos / 73 chunks, segunda execução 0 gravações).
O smoke achou um defeito que os testes não pegavam: o DDL do FTS (coluna `search_vector`, GIN, índice de escopo) só
rodava no caminho de testes (`CreateIndexes`), não no `-migrate` do servidor (`getIndexes`). Corrigido: as duas rotas
leem a mesma lista, com teste de regressão.

## Decisões da revisão (fechadas)

1. Papéis: só `admin` recebe `knowledge:read|write` no backfill.
2. Contexto: pertencimento do usuário; alcance global informa os parâmetros; fora do alcance = 403.
3. Manuais: somente CLI na 8A.
4. Acento: normalização no código, simétrica na indexação e na consulta, sem `unaccent`, preservando o texto original.
