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
| Quando o job para de checar um vínculo | `CANCELADO`/`perdida` imediatamente; `FECHADO` continua sendo consultado por mais 7 dias corridos a partir da **primeira vez** que o job viu `FECHADO` (cobre cancelamento pós-fatura) — só marca `resolved_at` quando esse prazo efetivamente passar, não no momento em que vê o primeiro `FECHADO`; nunca encontrado, sinaliza após 5 **rodadas do job** mas continua tentando | Evita checar pra sempre um vínculo já resolvido, sem fechar cedo demais a janela de cancelamento |
| Onde fica a credencial do X2 | Nova aba "ERP X2" em Configurações → Integrações, por organização, criptografada em repouso | Mesmo padrão já usado por `WhatsAppAccount.AccessToken`; nenhuma aba existente hoje serve pra "credencial que o Whatc usa pra chamar outro sistema" |
| Chave do X2 dentro de fluxos do bot | Nunca digitada no fluxo — nó `api_call` referencia uma variável especial resolvida só no backend | Evita duplicar o segredo em JSONB sem criptografia, espalhado por N fluxos |
| Trocar número/documento depois de registrado | Enquanto o vínculo não resolveu (`resolved_at` nulo), o formulário sobrescreve os mesmos campos. Depois de resolvido, não pode ser sobrescrito — só "registrar novo vínculo", que cria uma linha nova em `sales_opportunity_xprocess_links` (a antiga fica intacta) | Sobrescrever um vínculo já resolvido apagaria o rastro de uma conciliação que já aconteceu; a oportunidade em si só permite editar `xprocess_num_pedido`/`xprocess_documento` de novo depois que o vínculo anterior chegou a um estado terminal |
| Validação do documento no cadastro | Estrutural apenas: normaliza pra só dígitos, exige exatamente 11 (CPF) ou 14 (CNPJ); qualquer outro tamanho é rejeitado antes de salvar | Não consulta Receita nem X2 — coerente com "nenhuma chamada ao X2 no momento do registro" (linha acima); só pega erro de digitação óbvio |
| Números 5 (rodadas) / 7 (dias) / 1x-dia | Ponto de partida bom, mas tratados como parâmetros nomeados no código (constantes/config), não espalhados feito números mágicos | Facilita ajustar depois de operar por um tempo, sem virar uma tela de configuração nova agora (YAGNI) |

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
| `xprocess_num_pedido` | string, nulo | Sempre reflete o vínculo mais recente (§6) — conveniência de leitura, a fonte de verdade é `sales_opportunity_xprocess_links` |
| `xprocess_documento` | string, nulo | CPF ou CNPJ informado pelo agente junto com o número — dígitos, sem máscara; validado estruturalmente (11 ou 14 dígitos) antes de salvar, tanto aqui quanto no link |

### `sales_opportunity_xprocess_links` (nova tabela — histórico/estado da conciliação)

**Não é 1:1 com a oportunidade** — é 1-para-muitos, mas com no máximo uma linha "em aberto" (`resolved_at` nulo) por oportunidade a qualquer momento. Enquanto essa linha está em aberto, registrar de novo no formulário **atualiza a mesma linha** (mesmo `id`, `num_pedido`/`documento` sobrescritos). Depois que ela resolve (`resolved_at` preenchido), registrar de novo cria uma **linha nova**, preservando a anterior intacta — nunca sobrescreve um vínculo já conciliado.

| Campo | Tipo | Notas |
|---|---|---|
| `sales_opportunity_id` | uuid, índice | Uma oportunidade pode ter várias linhas ao longo do tempo, mas só uma com `resolved_at` nulo por vez |
| `num_pedido` / `documento` | string | `documento` normalizado só-dígitos, validado como CPF (11) ou CNPJ (14) antes de gravar |
| `cod_empresa` / `cod_vendedor` | string, nulo | Preenchidos a partir da primeira resposta 200 do X2 |
| `status_xprocess` | string, nulo | Último status visto (`SEPARACAO`/`SEPARADO`/`FECHADO`/`CANCELADO`) |
| `valor_vendido` / `itens` (JSONB) | numeric / JSONB, nulo | Dados reais do pedido, nunca sobrescrevem `estimated_value`/`estimated_quantity` da oportunidade |
| `last_checked_at` | timestamp | Atualizado a cada rodada do job, mesmo quando o resultado não muda |
| `first_closed_at` | timestamp, nulo | Gravado **uma única vez**, na primeira rodada em que o job vê `status=FECHADO` para este vínculo. Nunca reescrito nas rodadas seguintes — é a partir dele, não de `last_checked_at`, que se contam os 7 dias de carência (§5) |
| `consecutive_not_found` | int, default 0 | Conta **rodadas do job em que este vínculo respondeu 404**, não dias corridos desde o registro — um pedido registrado numa sexta só bate 404 nas rodadas diárias seguintes, então "5" normalmente significa ~5 dias úteis do job, não 5 dias desde o cadastro. Zera a cada resposta 200 |
| `resolved_at` | timestamp, nulo | Nulo = job continua checando; preenchido quando o vínculo chega a um estado terminal (§3) |

## 5. Job diário de conciliação

Roda uma vez por dia, depois das ~2h. Para cada `sales_opportunity_xprocess_links` com `resolved_at` nulo, chama `POST /api/pedido {numero_pedido: num_pedido, documento: documento}` com a credencial da organização (`xprocess_integrations`).

- **404** → `consecutive_not_found += 1`. A partir de 5 **rodadas** (não 5 dias corridos desde o cadastro — se o pedido foi registrado numa sexta à tarde, a 1ª checagem só acontece no sábado de madrugada, então o contador avança uma vez por rodada do job, não por dia desde o registro), aparece numa lista de pendências (§8) com o texto **"Pedido ainda não localizado no X2 após 5 consultas"** — nunca "pedido inválido": o desenho já reconhece que 404 tem duas causas possíveis (número/documento errado, ou pedido legítimo que ainda não chegou na carga D-1), e o texto não deve induzir o agente a "corrigir" um dado que pode estar certo. Não para de tentar sozinho, sem teto de tentativas — o custo é uma chamada por dia por vínculo pendente.
- **200, `status` ∈ {`SEPARACAO`, `SEPARADO`}** → se a oportunidade ainda está `aberta`, converte (`conversion_source=xprocess`, grava `SalesOpportunityEvent{type: converted, source: xprocess}`); atualiza o link com valor/itens/`cod_vendedor`; continua checando.
- **200, `status = FECHADO`** → mesma regra de conversão se ainda não convertida; atualiza o link. Se `first_closed_at` ainda está nulo, grava-o com o horário desta rodada (só na primeira vez — rodadas seguintes não o reescrevem). **O vínculo continua sendo selecionado pelo job** (critério permanece `resolved_at IS NULL`) durante os 7 dias seguintes a `first_closed_at` — inclusive pra pegar um eventual `CANCELADO` nesse meio-tempo, tratado imediatamente pela regra abaixo. Só quando uma rodada roda **depois** de `first_closed_at + 7 dias` (e o status continua `FECHADO`) é que o job preenche `resolved_at` com o horário dessa rodada, parando de consultar dali em diante. `resolved_at` nunca é calculado/gravado como `first_closed_at + 7 dias` diretamente na rodada que viu o primeiro `FECHADO` — isso tiraria o vínculo da consulta antes do prazo de fato ter passado.
- **200, `status = CANCELADO`** → se a oportunidade nunca convertida, marca **perdida** (`loss_reason` fixo, ex. `outro`, com `loss_notes` explicando "cancelado no X2"); se já convertida, marca **cancelada** com evento novo. Marca `resolved_at` imediatamente — não há mais transição possível depois de cancelado.

Valores numéricos do X2 vêm como string com vírgula decimal (ex. `"230,0418"`) — parsear trocando `,` por `.` antes de converter pra `float64`.

Resumo de `resolved_at` por situação:

| Situação | `resolved_at` |
|---|---|
| 404 (não encontrado) | `NULL` (continua tentando, sem teto) |
| `SEPARACAO` / `SEPARADO` | `NULL` (converteu, mas continua acompanhando até fechar ou cancelar) |
| `FECHADO` | `NULL` enquanto `agora < first_closed_at + 7 dias` (job continua consultando); preenchido só na primeira rodada em que `agora >= first_closed_at + 7 dias` |
| `CANCELADO` (nunca convertida → perdida) | Imediato |
| `CANCELADO` (já convertida → cancelada) | Imediato |

## 6. Agente registra o pedido

Na tela da oportunidade, formulário inline (mesmo padrão de edição de `estimated_value` via `PUT .../details`, `internal/handlers/sales_opportunities.go`): dois campos, número do pedido e documento (CPF/CNPJ). Só habilitado enquanto `status=aberta`. Validação **estrutural** do documento antes de salvar — normaliza pra só dígitos, exige 11 (CPF) ou 14 (CNPJ) dígitos exatos, rejeita qualquer outro tamanho com 400. Não valida na Receita nem no X2 (coerente com "nenhuma chamada ao X2 no momento do registro", abaixo).

Ao salvar:
- **Sem vínculo em aberto para esta oportunidade:** cria a linha em `sales_opportunity_xprocess_links`.
- **Já existe um vínculo em aberto (`resolved_at` nulo):** atualiza essa mesma linha (o agente corrigindo um número/documento digitado errado antes de qualquer conciliação acontecer).
- **O único vínculo existente já está resolvido (`resolved_at` preenchido):** o formulário não sobrescreve — oferece "registrar novo vínculo", que cria uma linha nova (§4), preservando a resolvida.
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
- Lista de pendências (vínculos com `consecutive_not_found >= 5`) **decidida para esta entrega: exposta na própria tela da oportunidade**, não um widget de dashboard novo — o agente já está olhando o pedido pendente ali, com todo o contexto. Um painel centralizado de exceções fica pra depois, só se o volume de casos justificar (YAGNI).

## 9. Verificação

**Go**
- Registrar número+documento sem vínculo prévio cria o link; registrar de novo com o vínculo ainda em aberto atualiza a mesma linha (mesmo `id`); registrar de novo com o único vínculo já resolvido cria uma linha **nova**, sem tocar na antiga.
- Documento com 10 ou 12 dígitos (nem CPF nem CNPJ) é rejeitado com 400 antes de gravar; com máscara (pontos/traço/barra) é normalizado e aceito.
- Job: 404 incrementa `consecutive_not_found` sem mudar estado da oportunidade; na 5ª rodada seguida com 404 (não no 5º dia corrido — simular rodadas não-diárias-consecutivas do teste pra confirmar que é por rodada) aparece na lista de pendências com o texto "não localizado... após 5 consultas", nunca "inválido".
- Job: `SEPARACAO` converte oportunidade `aberta`; não reconverte uma já `convertida`; grava evento com `source=xprocess`.
- Job: `FECHADO` sem conversão prévia também converte; com conversão prévia, só atualiza o link.
- Job: `first_closed_at` é gravado só na primeira rodada que vê `FECHADO`; uma segunda rodada vendo `FECHADO` de novo (antes do vínculo resolver) não reescreve `first_closed_at`, e `resolved_at` continua calculado a partir do valor original, não do `last_checked_at` da segunda rodada.
- Job: `CANCELADO` sem conversão prévia → perdida; com conversão prévia → cancelada, evento novo, `converted` original preservado.
- Job: `resolved_at` fica nulo até o estado terminal certo — imediato pra cancelado/perdida; pra fechado, continua **nulo** em toda rodada anterior a `first_closed_at + 7 dias` (inclusive na própria rodada que gravou `first_closed_at`), e só é preenchido na primeira rodada igual ou posterior a esse prazo; sem teto pra "ainda não encontrado".
- Job: um `CANCELADO` que aparece numa rodada dentro da janela dos 7 dias (vínculo já convertido, `resolved_at` ainda nulo por estar em `FECHADO`) é processado como cancelamento imediatamente, sem esperar o prazo dos 7 dias terminar.
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
| Agente digita número ou documento errado | Contador `consecutive_not_found` sinaliza depois de 5 rodadas do job (não necessariamente 5 dias corridos desde o registro), com texto neutro ("não localizado", não "inválido") já que 404 também é esperado pra pedido legítimo ainda não pago |
| Agente troca o número/documento depois do vínculo já ter conciliado | Vínculo resolvido é imutável; trocar cria um vínculo novo, o antigo fica no histórico |
| `/api/vendas` sem paginação/filtro de data (limite 1000) | Não é usada por este desenho (só `/api/pedido`, consulta exata); só voltaria a importar numa eventual varredura por aproximação, fora de escopo |
| Documento sensível exposto na conversa do WhatsApp | Bot nunca ecoa o documento inteiro de volta, só os últimos dígitos |
| Job perde uma rodada (servidor fora do ar às 2h) | Cada vínculo mantém seu próprio estado (`resolved_at` nulo); a próxima rodada simplesmente pega de onde parou, sem lógica de "recuperar dia perdido" necessária |
| Conversão automática divergir de uma conversão manual já feita pelo agente no mesmo dia | Job só age sobre oportunidades ainda `aberta` na direção `aberta→convertida`; uma conversão manual já feita antes do job rodar não é revertida nem duplicada |
