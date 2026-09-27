# Central de Vendas — Entrega 2: Conciliação com o ERP X2/XProcess (design)

## 1. Contexto

A Entrega 1 (`docs/superpowers/specs/2026-09-17-central-vendas-funil-entrega1-design.md`, em produção) cobre o funil de oportunidades com conversão **manual** pelo agente. Esta entrega adiciona a conciliação automática com o ERP X2 (API pública em `https://api.atacadaodospisos.com.br/docs`, apelidada de "XProcess" no schema desde a Entrega 1 — `SalesConversionSource.xprocess`, `SalesOpportunityEventSource.xprocess`, `User.XProcessSellerCode`), mais um bot de autoatendimento de status de pedido que reaproveita a mesma integração.

**A API do X2 não é transacional.** Foi construída pelo próprio time do cliente a partir de arquivos CSV, exportados do X2 e recarregados no banco de dados que serve a API todo dia, por volta das 2h da madrugada. Ou seja: **é sempre D-1** — qualquer coisa que aconteça no X2 hoje só aparece na API amanhã de manhã. Isso não é uma limitação a contornar, é uma restrição estrutural que molda todo o desenho abaixo (job diário, nunca em tempo real; mensagens ao cliente deixando claro "até o fechamento de ontem").

**Ciclo de vida do pedido no X2** (confirmado com o dono do produto): o agente monta o carrinho no X2 durante ou depois da conversa no WhatsApp — nasce um número de pedido, mas o carrinho **não aparece na API** enquanto não for pago. Quando o financeiro confirma o pagamento, o pedido passa a existir na API com status `SEPARACAO`/`SEPARADO` (aguardando separação/NFe). O status final `FECHADO` só chega quando a NFe é de fato emitida — o que pode levar dias, dependendo do agendamento de entrega com o cliente ou da logística. Por isso a conversão em "Convertida" no Whatc **não espera `FECHADO`** — ver §5.

**Descoberta de API relevante para o desenho** (validada com chamadas reais, usando uma chave de teste, nenhum dado gravado):
- `GET /api/vendas` retorna uma linha por **item** do pedido (não por pedido), agrupável por `cod_empresa`+`num_pedido`. Só aceita `cod_cliente`, `cod_vendedor` e `limit` (máx. 1000) como filtro — **sem filtro de data, sem paginação além do limite**. Não é adequado para uma varredura ampla e periódica.
- `POST /api/pedido` (`{numero_pedido, documento}`) é uma consulta exata e validada: `numero_pedido` sozinho é ambíguo (**não é único globalmente, só por loja** — confirmado: o mesmo número `666` resolveu para dois pedidos completamente diferentes, de lojas diferentes, nas nossas chamadas de teste). Passar `documento` (CPF **ou CNPJ** — a API atende ambos, `tipo_pessoa` vem como `F`/`J` na resposta) desambigua e valida: documento errado devolve `404 "Pedido nao encontrado"` mesmo com o número certo.
- Documento (CPF/CNPJ) é **obrigatório** no cadastro do cliente no X2 — sempre existe lá, mesmo que o `Contact.CPFCNPJ` do Whatc esteja vazio (hoje só é preenchido durante abertura de protocolo de SAC, normalmente vazio em conversas só de vendas).
- Status observados: `SEPARACAO`, `SEPARADO`, `FECHADO`, `CANCELADO`.

## 2. Objetivos e não-objetivos

**Objetivos:**
- Agente registra o número do pedido do X2 (+ documento do cliente) na oportunidade, a qualquer momento enquanto ela está `aberta`.
- Job diário concilia esses registros com a API do X2 e converte/perde/cancela a oportunidade automaticamente, sem ação manual.
- Tela de configuração da credencial do X2, por organização.
- Bot de autoatendimento: cliente pergunta o status do pedido (por documento, listando recentes, ou por número específico), o Whatc responde consultando o X2.

**Não-objetivos (fora desta entrega, de propósito — YAGNI):**
- **Varredura por aproximação** (casar por vendedor+cliente+janela de tempo, fila de exceção para casos ambíguos) — o registro explícito do número do pedido, validado por documento, é preciso o suficiente; a varredura só se justifica se, na prática, muitos agentes esquecerem de registrar o número. Revisar depois de medir.
- **Reformulação do roteamento Unidade×Equipe×Agente** (hoje o nó de transferência do fluxo tem `team_id` fixo por ramo; `Unit` existe mas só é lida pelo módulo de Ocorrências). É um projeto independente, sem relação com o X2 — não misturar nesta entrega.
- Qualquer escrita no X2 (o Whatc nunca cria/altera pedido lá — só lê).

## 3. Decisões

| Decisão | Escolha | Motivo |
|---|---|---|
| Quando marcar "Convertida" | Ao ver `SEPARACAO`/`SEPARADO` (pagamento confirmado) | `FECHADO` só chega quando a NFe é emitida, o que pode levar dias após o pagamento — esperar isso atrasaria a conversão sem necessidade |
| Chave de conciliação | `num_pedido` + `documento` (CPF ou CNPJ) | `num_pedido` sozinho não é único entre lojas; `documento` é obrigatório no X2 e a própria API já valida a combinação (404 se não bater) |
| Onde o agente digita o documento | Formulário próprio na oportunidade, não depende de `Contact.CPFCNPJ` | Hoje esse campo normalmente está vazio fora do fluxo de SAC; o agente acabou de digitar o documento no X2 mesmo, é só pedir de novo ali |
| `Contact.CPFCNPJ` | Preenchido a partir do documento informado, se estiver vazio | Aproveita o dado pra outras telas (SAC) sem re-perguntar depois |
| Validação do número na hora do registro | **Não existe** — só salva | A base é D-1; validar na hora quase sempre devolveria "não encontrado" por o registro ainda não ter chegado no X2, confundindo o agente com um falso erro |
| Frequência do job | Uma vez por dia, após ~2h (mesmo horário do processamento dos CSVs no lado do X2) | Os dados não mudam entre uma rodada e outra do mesmo dia; consultar mais vezes não traria nada de novo |
| `FECHADO` sem conversão prévia | Também converte (mesma regra de `SEPARACAO`) | Pode acontecer de o job nunca ter visto o pedido em `SEPARACAO` (ex.: atraso no registro do agente); `FECHADO` é estritamente "mais confirmado" que `SEPARACAO` |
| `CANCELADO` sem conversão prévia | Marca **perdida** | Do ponto de vista do Whatc, nunca houve venda confirmada — é uma oportunidade que não vingou |
| `CANCELADO` após já ter convertido | Marca **cancelada**, grava evento novo, não apaga o `converted` original | Histórico preserva que a venda existiu e foi revertida depois (mesma regra já prevista na Entrega 1 §3) |
| Quando o job para de checar um vínculo | `CANCELADO`/`perdida` imediatamente; `FECHADO` após mais 7 dias de graça (cobre cancelamento pós-fatura); nunca encontrado, sinaliza após 5 tentativas mas continua tentando | Evita checar pra sempre um vínculo já resolvido, sem fechar cedo demais a janela de cancelamento |
| Onde fica a credencial do X2 | Nova aba "ERP X2" em Configurações → Integrações, por organização, criptografada em repouso | Mesmo padrão já usado por `WhatsAppAccount.AccessToken`; nenhuma aba existente hoje serve pra "credencial que o Whatc usa pra chamar outro sistema" |
| Chave do X2 dentro de fluxos do bot | Nunca digitada no fluxo — nó `api_call` referencia uma variável especial resolvida só no backend | Evita duplicar o segredo em JSONB sem criptografia, espalhado por N fluxos |

## 4. Modelo de dados

### `xprocess_integrations` (nova tabela)

| Campo | Tipo | Notas |
|---|---|---|
| `organization_id` | uuid, único | Uma credencial por organização |
| `base_url` | string | Pré-preenchido com a URL atual, editável |
| `api_key` | string, criptografado (`crypto.EncryptFields`/`DecryptFields`) | Nunca retorna em claro pela API depois de salvo |
| `is_active` | bool | Desliga a integração sem apagar a credencial |

### `sales_opportunities` (colunas novas)

| Campo | Tipo | Notas |
|---|---|---|
| `xprocess_num_pedido` | string, nulo | Preenchido pelo agente |
| `xprocess_documento` | string, nulo | CPF ou CNPJ informado pelo agente junto com o número — dígitos, sem máscara, mesmo padrão de `Contact.CPFCNPJ` |

### `sales_opportunity_xprocess_links` (nova tabela — histórico/estado da conciliação)

| Campo | Tipo | Notas |
|---|---|---|
| `sales_opportunity_id` | uuid, índice | 1:1 com a oportunidade (criado junto quando o agente registra o número) |
| `num_pedido` / `documento` | string | Cópia do que foi registrado, para auditoria mesmo se o campo na oportunidade mudar depois |
| `cod_empresa` / `cod_vendedor` | string, nulo | Preenchidos a partir da primeira resposta 200 do X2 |
| `status_xprocess` | string, nulo | Último status visto (`SEPARACAO`/`SEPARADO`/`FECHADO`/`CANCELADO`) |
| `valor_vendido` / `itens` (JSONB) | numeric / JSONB, nulo | Dados reais do pedido, nunca sobrescrevem `estimated_value`/`estimated_quantity` da oportunidade |
| `last_checked_at` | timestamp | Atualizado a cada rodada do job |
| `consecutive_not_found` | int, default 0 | Zera a cada resposta 200; incrementa a cada 404 |
| `resolved_at` | timestamp, nulo | Nulo = job continua checando; preenchido quando o vínculo chega a um estado terminal (§3) |

## 5. Job diário de conciliação

Roda uma vez por dia, depois das ~2h. Para cada `sales_opportunity_xprocess_links` com `resolved_at` nulo, chama `POST /api/pedido {numero_pedido: num_pedido, documento: documento}` com a credencial da organização (`xprocess_integrations`).

- **404** → `consecutive_not_found += 1`. A partir de 5, aparece numa lista de pendências (ver §8) — não para de tentar sozinho, só sinaliza pra revisão humana (número ou documento possivelmente errados). Deliberadamente sem teto de tentativas: o custo de continuar checando é uma chamada por dia por vínculo pendente, e um pedido registrado cedo demais (antes do pagamento) é um caso legítimo, não um erro.
- **200, `status` ∈ {`SEPARACAO`, `SEPARADO`}** → se a oportunidade ainda está `aberta`, converte (`conversion_source=xprocess`, grava `SalesOpportunityEvent{type: converted, source: xprocess}`); atualiza o link com valor/itens/`cod_vendedor`; continua checando.
- **200, `status = FECHADO`** → mesma regra de conversão se ainda não convertida; atualiza o link; se já resolvido antes por `SEPARACAO`, apenas atualiza os dados. Marca `resolved_at` **7 dias depois** deste primeiro `FECHADO` (grace period pra cancelamento pós-fatura), não imediatamente.
- **200, `status = CANCELADO`** → se a oportunidade nunca convertida, marca **perdida** (`loss_reason` fixo, ex. `outro`, com `loss_notes` explicando "cancelado no X2"); se já convertida, marca **cancelada** com evento novo. Marca `resolved_at` imediatamente — não há mais transição possível depois de cancelado.

Valores numéricos do X2 vêm como string com vírgula decimal (ex. `"230,0418"`) — parsear trocando `,` por `.` antes de converter pra `float64`.

## 6. Agente registra o pedido

Na tela da oportunidade, formulário inline (mesmo padrão de edição de `estimated_value` via `PUT .../details`, `internal/handlers/sales_opportunities.go`): dois campos, número do pedido e documento (CPF/CNPJ). Só habilitado enquanto `status=aberta`. Ao salvar:
- Cria (ou atualiza, se já existia) a linha em `sales_opportunity_xprocess_links`.
- Se `Contact.CPFCNPJ` da oportunidade estiver vazio, preenche com o documento informado.
- **Nenhuma chamada ao X2 acontece nesse momento.** Mensagem de confirmação: "Registrado — a confirmação automática roda amanhã de manhã."

## 7. Bot — consulta de status de pedido

Dois caminhos no construtor de fluxos, usando o nó `api_call` já existente (`internal/handlers/chatbot_graph_runner.go`) com uma variável nova, resolvida só no servidor: `{{integrations.xprocess.api_key}}` — nunca aparece no JSON do fluxo nem na tela de edição.

- **Por documento:** `GET /api/clientes?documento=<doc>` confirma identidade e retorna `cod_cliente` → `GET /api/vendas?cod_cliente=<id>` lista pedidos recentes → bot resume os status agrupando por `num_pedido`.
- **Por número do pedido:** pede também o documento (obrigatório — sem ele, `/api/pedido` fica ambíguo entre lojas e o Whatc não deve nem tentar) → `POST /api/pedido` → bot responde o status daquele pedido.

Toda resposta menciona a data de referência (D-1: "dados até o fechamento de ontem à noite"). Ao confirmar o documento de volta pro cliente na conversa, mostrar só os últimos dígitos (ex. `***.***.555-20`), nunca o documento inteiro — é dado sensível numa conversa de WhatsApp.

## 8. Permissões e observabilidade

- Novo recurso `xprocess_integration` (`ResourceXProcessIntegration`), ações `read`/`write`, mesmo padrão de `accounts`/`api_keys`. **Precisa de backfill** pra organizações existentes (lição da Fase 3 — `DefaultPermissions()` sozinho não alcança orgs já criadas).
- Leitura/escrita do vínculo (`sales_opportunity_xprocess_links`) segue a mesma permissão de `sales_opportunities` já existente — não é um recurso novo.
- Lista de pendências (vínculos com `consecutive_not_found >= 5`) exposta na própria tela da oportunidade ou como um widget novo no dashboard de vendas — a decidir na hora da implementação, sem impacto no modelo de dados acima.

## 9. Verificação

**Go**
- Registrar número+documento cria o link; registrar de novo (mesma oportunidade) atualiza em vez de duplicar.
- Job: 404 incrementa contador sem mudar estado da oportunidade; 5º 404 seguido aparece na lista de pendências.
- Job: `SEPARACAO` converte oportunidade `aberta`; não reconverte uma já `convertida`; grava evento com `source=xprocess`.
- Job: `FECHADO` sem conversão prévia também converte; com conversão prévia, só atualiza o link.
- Job: `CANCELADO` sem conversão prévia → perdida; com conversão prévia → cancelada, evento novo, `converted` original preservado.
- Job: `resolved_at` fica nulo até o estado terminal certo (imediato pra cancelado/perdida, +7 dias pra fechado, nunca pra "ainda não encontrado").
- Parsing de valores com vírgula decimal do X2.
- `/api/pedido` chamado sempre com `documento`; nunca chamado só com `num_pedido`.
- Permissão: usuário sem `xprocess_integration:write` não consegue salvar a credencial; sem `sales_opportunities:write` não consegue registrar número/documento.
- Bot: consulta por número sem documento informado não dispara a chamada (mensagem pedindo o documento primeiro).

**Manual / Playwright**
- Configurar credencial em Integrações → ERP X2, testar conexão, salvar.
- Registrar número+documento numa oportunidade aberta, confirmar mensagem "roda amanhã", sem chamada de rede na hora (checar via devtools/network).
- Fluxo do bot respondendo status por documento e por número, incluindo o caso de documento não encontrado.

## 10. Riscos conhecidos

| Risco | Mitigação |
|---|---|
| Agente digita número ou documento errado | Contador `consecutive_not_found` sinaliza depois de 5 tentativas diárias (5 dias) |
| `/api/vendas` sem paginação/filtro de data (limite 1000) | Não é usada por este desenho (só `/api/pedido`, consulta exata); só voltaria a importar numa eventual varredura por aproximação, fora de escopo |
| Documento sensível exposto na conversa do WhatsApp | Bot nunca ecoa o documento inteiro de volta, só os últimos dígitos |
| Job perde uma rodada (servidor fora do ar às 2h) | Cada vínculo mantém seu próprio estado (`resolved_at` nulo); a próxima rodada simplesmente pega de onde parou, sem lógica de "recuperar dia perdido" necessária |
| Conversão automática divergir de uma conversão manual já feita pelo agente no mesmo dia | Job só age sobre oportunidades ainda `aberta` na direção `aberta→convertida`; uma conversão manual já feita antes do job rodar não é revertida nem duplicada |
