# Agente de IA: pacote de homologação controlada

Branch `feature/agente-ia-producao`, HEAD `e25df7f8`. Este pacote consolida o que já existe
([roteiro](agentes-ia-roteiro.md), [preparação real](agentes-ia-preparacao-real.md), `evidence.sql`) e acrescenta
o que mudou depois: teto aplicado no runtime, isolamento por número (`ai_agent_only`), handoff durável e
endurecimento do cliente HTTP (SEC-1). **Não autoriza** push, PR, merge, deploy nem produção. Nenhuma mensagem
real foi enviada na preparação deste documento.

## 1. Insumos recebidos

| Item | Valor | Observação |
|---|---|---|
| Número de teste da Meta | +1 555-172-8886 | número de teste da Meta |
| Phone number ID | `947513168453524` | |
| WhatsApp Business Account ID | `25892620350389076` | |
| URL do webhook | `https://bot.atcpisos.com/api/webhook` é **produção: não usar na homologação** | falta uma URL HTTPS própria do ambiente isolado (outro subdomínio ou túnel) |
| App ID (Meta) | `1652697245841199` | |
| Destinatário autorizado | `5571996002928` (informado como 71 99600-2928, DDI 55) | o único aparelho do piloto; precisa estar na lista de destinatários do número de teste na Meta |
| App Secret (Meta) | **recebido, não consta aqui** | entra só pela tela da conta WhatsApp; também foi colado no chat: regenerar ao fim |
| Provedor de IA | Groq | provedor nativo (`groq`, API compatível com OpenAI, base `https://api.groq.com/openai/v1`); nenhum código novo |
| Token da Meta e chave da Groq | **não constam aqui** | entram só pela tela, na organização de teste (ver 2.4) |

**Faltam, e só você pode fornecer:**
1. **URL pública HTTPS** do servidor de homologação (túnel ou domínio) para o webhook da Meta.
2. Um **verify token** à sua escolha (o App Secret já foi recebido; ele assina o webhook em `X-Hub-Signature-256`).
3. **Modelo da Groq** a usar (o sistema lista os modelos da conta na própria tela depois de salvar a chave).
4. **Segundo aparelho** (opcional) para o cenário "número não autorizado"; sem ele o cenário é coberto por teste automatizado.

## 2. Pré-requisitos e montagem

### 2.1 Cuidado com as credenciais (leia antes)
- O token da Meta e a chave da Groq foram **colados no chat**. Trate-os como expostos: **revogue/gere novos** ao
  fim da homologação (ou agora, se preferir) e use-os só em organização de teste.
- O token temporário do painel da Meta expira em cerca de 24 h; para homologação mais longa gere um token de
  usuário do sistema.
- Nunca coloque tokens em arquivo versionado, em `config.toml` commitado, em log ou em capturas de tela.

### 2.2 Ambiente
1. Binário **limpo** da branch (sem ganchos de simulador, sem `base_url` apontando para fakes):
   `go build -o whatomate.exe ./cmd/whatomate`.
2. **Banco PostgreSQL e Redis exclusivos** da homologação (nunca produção nem desenvolvimento);
   `whatomate.exe server -migrate`. As migrações desta branch são aditivas (tabela `ai_handoff_intents`; colunas
   `ai_unlimited_ack` e `ai_agent_only`, padrão falso).
3. Servidor atrás de **HTTPS público**; ClamAV conforme a configuração padrão do projeto.
4. Confirmar que `[whatsapp] base_url` **não** está definido (usa a Graph API real) e que o ambiente não aponta
   para os servidores simulados (portas 9091/9092).

### 2.3 Meta
1. No aplicativo da Meta, produto WhatsApp: webhook = `https://<seu-host>/api/webhook` (GET para verificação, POST para eventos), verify token = o escolhido, campo `messages` assinado.
2. O número de teste só envia **para destinatários cadastrados** na lista da Meta. Cadastre `5571996002928`.
3. **Janela de 24 h:** mensagem livre só dentro de 24 h da última mensagem do cliente. Para abrir a conversa,
   o cliente (você) **escreve primeiro** ao número de teste. O `hello_world` do curl que você colou é modelo e
   serve só para abrir a janela; **não foi executado por mim**.

### 2.4 Organização de teste (pela tela)
1. Criar uma organização **nova e isolada**; usuários: um admin, um vendedor da equipe da unidade.
2. Conta WhatsApp: nome da conta, Phone ID `947513168453524`, WABA `25892620350389076`, token, App Secret e verify
   token (a tela criptografa os segredos).
3. Uma **unidade** com equipe ativa como equipe padrão do número; uma segunda equipe sem unidade ("fila geral").
4. Configurações → Chatbot: habilitar; IA habilitada com provedor Groq e a chave (colada **só na tela**);
   **limite mensal de tokens baixo** (ex.: 3.000: valor de teste, não recomendação comercial);
   **"Só números com agente" ligado** (isolamento).
5. Agentes de IA: um agente habilitado, marcado como padrão, com **apenas a conta do número de teste**.
6. Painel de prontidão do agente sem pendências.

## 3. Roteiro (execução e evidência)

Siga os cenários 1–10 do [roteiro](agentes-ia-roteiro.md) (seções 1 a 3b) e, **em acréscimo**, estes, que cobrem as
correções desta branch:

| # | Cenário | Resultado esperado |
|---|---|---|
| A1 | Teto atingido (consumir o limite) | agente não chama a IA; cliente vai a pessoas com nota "limite de IA atingido"; consumo na tela ≤ limite |
| A2 | Remover o limite sem marcar o aceite e tentar ativar/salvar o agente | a tela/API recusa (422); com o agente já ativo e sem decisão de teto, o runtime recusa a chamada e transfere ("limite mensal não configurado") |
| A3 | Marcar "usar sem limite" (aceite explícito) | agente volta a responder; nada é reservado |
| A4 | Mensagem de **outro número** da organização com "Só números com agente" ligado | nenhuma IA, nada dito, vai à fila humana |
| A5 | Mídia recebida (imagem/PDF reais) | download funciona (hosts da Meta); falha, se houver, é clara e **sem URL ou token** na mensagem |
| A6 | Encerrar o processo logo após a despedida da IA (`kill`) e subir de novo | em até ~3 min a transferência é criada, **um** atendimento, no máximo **uma** mensagem de espera |
| A7 | Envio com resultado incerto (derrubar a rede no envio) | texto "Não foi possível confirmar o envio…", **sem** URL da Graph API; nada reenviado sozinho |
| A8 | Usuário sem `chatbot.ai:read` abre as configurações | não recebe `ai_unlimited_ack`, `ai_agent_only`, teto nem agente padrão |
| A9 | Janela de 24 h: responder após 24 h sem mensagem do cliente | comportamento documentado (a Meta recusa mensagem livre); registrar o que o sistema mostra |
| A10 | Indicador de digitação | funciona; registrar se a Meta cobra (medição, não suposição) |

Evidência por cenário: `psql "$DATABASE_URL" -v phone=5571996002928 -f docs/homologacao/evidence.sql >
evidencias/cenario-N.txt`, mais capturas do chat e do CRM.

## 4. Critérios objetivos de aprovação

Aprova somente se **todos** forem verdadeiros:
1. Cenários 1–10 do roteiro e A1–A8 passam, cada um com evidência anexada.
2. Zero mensagem da IA para número fora da lista ou depois de humano assumir.
3. Zero mensagem com preço, estoque ou prazo afirmados como certos.
4. Zero oportunidade ou atendimento duplicado; zero transferência sem equipe/fila.
5. Consumo registrado ≤ limite configurado, medido na tela e no banco (`ai_usage_logs`).
6. Nenhum token, chave ou URL de API em log, mensagem de erro ou resposta de API.
7. Recuperação do handoff (A6) dentro de 3 min e sem duplicidade.
8. CI Linux verde na branch (ver 6) antes de qualquer integração.

## 5. Condições de parada (interrompa e avise)

- IA responde a número fora da lista, ou após humano assumir.
- Mensagem da IA sem assinatura, ou humana com assinatura de IA.
- Preço, estoque ou prazo afirmados como certos.
- Oportunidade duplicada, ou transferência sem equipe/fila.
- Consumo acima do teto.
- Qualquer indício de dado compartilhado com a organização de produção.
- Mensagem enviada a número que não é `5571996002928`.

## 6. CI Linux (pendente, necessária antes da integração)

A CI só roda em PR para `main` (não aberto). Cobre: `go test -race -p 1`, testes de symlink,
`TestHandoffRealKill_*` e Playwright. Falhas locais conhecidas, **preexistentes** (confirmadas no commit-base
7bdcc4e6): `TestServeLoginBackground_RejectsSymlink`, `TestApp_ServeMedia_RejectsSymlink` (privilégio de symlink
no Windows), `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder`; instável:
`TestScanIngress_RefusesInfectedAndUnscannedUploads`.

## 7. Plano de rollback

Em ordem, do menos ao mais drástico:
1. **Desabilitar o agente** na tela (a IA para; o chatbot normal segue). Imediato.
2. **Zerar o isolamento**: desligar "Só números com agente" se o chatbot normal precisar voltar nos demais números.
3. **Desligar a IA** em Configurações → Chatbot.
4. **Desconectar o webhook** na Meta e **revogar** o token da Meta e a chave da Groq.
5. **Descartar** o banco e o Redis da homologação.
6. Reversão de esquema (aditiva, só se necessário): `DROP TABLE ai_handoff_intents; ALTER TABLE chatbot_settings
   DROP COLUMN ai_unlimited_ack, DROP COLUMN ai_agent_only;`.
7. Nada foi enviado a `origin`: a branch é local; produção não é tocada.

## 8. Riscos conhecidos durante a homologação

Lista fixa de hosts da Meta para download; recuperação do handoff com atraso de até ~3 min; sem política de
retenção de `ai_pending_replies`/`ai_handoff_intents`; `GET /chatbot/settings` ainda expõe prompt, provedor e
modelo a usuários da organização; outros clientes HTTP (webhooks, ações customizadas) sem endurecimento.
Tratar os dois últimos antes de produção.

## 9. Autorizações que continuam pendentes

Executar a homologação real (enviar mensagens), push, PR, merge, deploy, produção.
