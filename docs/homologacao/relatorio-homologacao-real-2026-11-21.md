# Relatório da homologação real do Agente de IA (2026-11-21)

Branch `feature/agente-ia-producao`. Homologação **controlada**: um número de teste da Meta, um único destinatário
autorizado, organização isolada, banco e Redis exclusivos, provedor Groq. Nenhuma mensagem foi enviada a clientes
reais e nada foi enviado a `origin` (sem push, PR, merge ou deploy). Credenciais (token da Meta, App Secret, verify
token, chave da Groq) foram informadas só pela tela/script local e **não constam** neste repositório.

## 1. Ambiente

| Item | Valor |
|---|---|
| Binário | build limpo da branch, sem ganchos de simulador, API real da Meta e da Groq |
| Banco / Redis | `agiaprod_homolog` e Redis db 10, exclusivos (nunca produção) |
| Entrada do webhook | túnel público temporário para esta máquina (não o domínio de produção) |
| Número / conta | número de teste da Meta; um destinatário autorizado |
| Organização | uma unidade com equipe "Comercial Teste"; equipe "Fila geral"; agente habilitado, padrão, só neste número |
| Modelo | `openai/gpt-oss-120b` (início), depois `qwen/qwen3.8-27b` |
| Teto | 3.000 → 20.000 → 30.000 tokens (valores de teste, não recomendação comercial) |
| Isolamento | "Só números com agente" ligado |

## 2. Resultados por cenário

| Cenário | Resultado | Evidência |
|---|---|---|
| Webhook verificado pela Meta (token da conta) | Passou | log "verified successfully (account token)" |
| Mensagem do cliente recebida e processada | Passou | contato e mensagem no banco |
| Resposta do agente entregue ao aparelho | Passou | mensagem `outgoing` com status `read` |
| Memória da conversa (sem repetir perguntas) | Passou, após correção | ver achado 2 |
| Qualificação extraída (produto, ambiente, metragem, intenção) | Passou com `qwen`; falhou com `gpt-oss` | ver achado 3 |
| Transferência à equipe da unidade, uma mensagem de espera | Passou | transferência `ai_agent`, equipe "Comercial Teste" |
| Silêncio com atendimento ativo | Passou | sem resposta e sem consumo de IA |
| Preço do m² | Passou | agente remete ao vendedor, sem valor |
| Estoque | Passou | pede detalhes e transfere para o vendedor confirmar |
| Prazo de entrega | Passou | "o prazo é confirmado pelo vendedor" |
| Desconto de 10% | Passou | "o vendedor confirma as condições e descontos" |
| Teto atingido | Passou | `cap_reached`, 0 tokens, transferência com nota "limite atingido" |
| Oportunidade no CRM refletindo a qualificação | Falhou, corrigido | ver achado 4 |

**Não testados nesta rodada:** reserva ("reserva 40 m² agora"), vendedor humano assumindo, isolamento por outro
número, mídia real, queda real do processo, janela de 24 h, cobrança do indicador de digitação.

## 3. Achados e correções

| # | Achado | Causa | Correção | Teste |
|---|---|---|---|---|
| 1 | Recusa definitiva da Meta (`131030`, destinatário fora da lista) aparecia como "pode ter chegado" | o código não estava na lista de recusas documentadas | `131030` entra em `refusedCodes` | `TestClassify_*` (errors_test) |
| 2 | O agente repetia perguntas já respondidas | histórico padrão de 4 mensagens; sem campo na tela/API | agente vê no mínimo 20 mensagens (`aiAgentMinHistory`), mesmo com `IncludeHistory` desligado; IA sem agente mantém a regra antiga | `TestGenerateAIReply_AgentSeesAtLeastTwentyMessagesOfHistory` |
| 3 | Extração falhava com modelo de raciocínio ("no JSON object") | limite de 600 tokens consumido pelo raciocínio | orçamento da extração 600 → 2000 (`aiQualificationMaxTokens`) | `TestHandoffFromAI_ExtractionRequestsRoomForReasoningModels` |
| 4 | Oportunidade aberta ficava vazia mesmo com qualificação boa depois | retrigger de oportunidade aberta nunca reescreve | completa **só** a oportunidade ainda com o texto vazio da extração que falhou; edição de pessoa nunca é sobrescrita | `TestHandoffFromAI_LaterGoodQualificationCompletesTheBareOpportunity`, `…OpportunityEditedByAPersonIsNeverOverwritten` |
| 5 | "Já passei a ele o que conversamos" mesmo com extração falha | texto escolhido só pelo motivo da transferência | texto longo só quando houve qualificação; senão, o curto | `TestHandoffFromAI_FailedExtractionDoesNotPromiseASummary` |
| 6 | Resposta imediata, sem esperar o cliente terminar de escrever | `wait_seconds` do agente em 0 (padrão "responde na hora"); o controle de espera **já existe** (PR #154) | novo agente na tela nasce com 8 s; o agente da homologação foi ajustado para 8 s | `AIAgentsView.spec.ts` ("starts a new agent with an 8 second wait") |

### Decisões e pontos não alterados

- **Duas mensagens na transferência** (despedida do agente e "Vou chamar um atendente"): mantido de propósito. A mensagem
  do sistema só sai depois que o atendimento existe (P1-01); a despedida do modelo não pode ser interpretada. Fica
  como ajuste de texto/UX futuro.
- **Histórico da IA sem agente** (`ai_history_limit`): continua sem campo na tela/API; só afeta a IA livre da
  organização. Agentes não dependem mais dele.
- **Agentes criados só por API** ficam com espera 0 (comportamento anterior preservado): defina `wait_seconds`.
- **Efeito do orçamento maior na extração:** a reserva de tokens do teto estima pelo máximo, então perto do limite a
  extração pode ser recusada antes; nesse caso a transferência acontece sem resumo (mensagem curta, achado 5).

## 4. Consumo observado

Respostas do agente: cerca de 430 a 750 tokens; extração: 575 a 1.327 tokens. Uma conversa de qualificação completa
(5 a 6 respostas e a extração) consome em torno de 3.000 a 4.000 tokens. Esses números servem de insumo para definir o
teto; o valor comercial continua sendo decisão sua.

## 5. Riscos e pendências

- **CI Linux** (só roda em PR para `main`, não aberto): `-race`, testes de symlink, `TestHandoffRealKill_*`, E2E.
- **Falhas locais preexistentes** (confirmadas no commit-base): dois testes de symlink (privilégio do Windows) e
  `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder`; instável: `TestScanIngress_RefusesInfectedAndUnscannedUploads`.
- Janela de 24 h e cobrança do indicador de digitação: sem prova.
- Lista fixa de hosts da Meta para download; recuperação do handoff com atraso de até ~3 min; sem retenção de
  `ai_pending_replies` e `ai_handoff_intents`; `GET /chatbot/settings` ainda expõe prompt, provedor e modelo a usuários da
  organização; demais clientes HTTP sem endurecimento.
- **Credenciais expostas no chat:** regenerar o token da Meta, o App Secret e a chave da Groq ao fim da homologação.

## 6. Veredito

**Aprovado com ressalvas** para continuar a homologação. O núcleo funcionou ponta a ponta com a Meta e a Groq reais:
resposta, memória, qualificação, transferência, silêncio, segurança comercial (preço, estoque, prazo, desconto) e teto.
Os seis achados foram corrigidos com teste. Faltam os cenários não testados e a CI Linux antes de qualquer integração.
