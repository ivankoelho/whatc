# Fechamento de segurança: SSRF em URLs de mídia, URA legada e campanhas antigas

Continua `2026-10-27-antimalware-cobertura-e-homologacao.md` (a matriz foi atualizada). A política segue **fail-closed** e a decisão de arquivos guardados continua em `internal/handlers/filescan.go`.

## 1. SSRF: obtenção de arquivos remotos

### Rotas e chamadas auditadas

Todas as chamadas HTTP de saída do repositório foram listadas (`http.Get/NewRequest/Client.Do`):

| Chamada | Origem da URL | Obtém arquivo? | Situação |
|---|---|---|---|
| `messages.go` cabeçalho de template por URL (`header_media_url`) | **digitada por pessoa** | sim (mídia) | **corrigida**: cliente seguro (`internal/safehttp`) + varredura antes do envio |
| `pkg/whatsapp` `DownloadMedia` / Flow JSON | URL devolvida pela Graph API da Meta (autenticada, TLS) | sim (mídia de clientes) | exceção justificada **EX-5**; o conteúdo é varrido depois de baixado (linha 4 da matriz) |
| Webhooks, custom actions, chamada de API em fluxo de chatbot (`webhook_dispatch`, `custom_actions`, `chatbot_processor`) | configurada por administrador | não (chamadas de API/eventos) | já usavam `SSRFSafeDialer`; agora usa **a mesma política de endereços** do cliente seguro (CGNAT, reservados, metadados, IPv4-mapped/NAT64/6to4/Teredo) |
| `internal/calling/http_callback.go` (nó "HTTP callback" da URA) | configurada por administrador | não (≤64 KB de texto; só ramifica por status) | **EX-6** (ver abaixo); não alterada |
| `internal/ai/http.go` (provedor de IA) | configurada por administrador | não | fora do escopo de mídia; endpoints de provedor |
| `pkg/xprocess`, `sso.go`, `accounts.go` (Meta Graph), `xprocess_integration.go` | endereço de integração configurado por administrador / API conhecida | não | fora do escopo de mídia |
| Nextcloud | servidor da organização (pode ser rede privada por política própria) | sim (arquivo de pessoa) | **política própria, não reaproveitada**; arquivos passam pelo ClamAV |

### O cliente seguro (`internal/safehttp`)

- **Somente HTTPS, porta 443**, sem credenciais na URL; recusa `localhost`, `*.localhost`, `*.local`, `*.internal`, `metadata.google.internal`.
- **Resolve o nome e exige que todas as respostas sejam públicas** (uma resposta ruim recusa o host inteiro); depois **conecta direto ao IP validado**, então uma segunda resposta de DNS nunca muda o destino (**DNS rebinding**). O endereço é revalidado **no momento da conexão** (`net.Dialer.Control`), a cada conexão, inclusive em cada redirecionamento.
- **Bloqueados:** `0/8`, `10/8`, `100.64/10` (CGNAT; inclui o metadado da Alibaba), `127/8`, `169.254/16` (link-local, inclui 169.254.169.254), `172.16/12`, `192.0.0/24`, `192.0.2/24`, `192.168/16`, `198.18/15`, `198.51.100/24`, `203.0.113/24`, multicast, `240/4` e broadcast; IPv6 `::`, `::1`, `fc00::/7` (inclui o metadado AWS `fd00:ec2::254`), `fe80::/10`, `ff00::/8`, `2001:db8::/32`, Teredo, e endereços IPv4-mapeados, NAT64 (`64:ff9b::/96`) e 6to4 julgados pelo IPv4 embutido.
- **Redirecionamentos:** no máximo 3, cada salto sob as mesmas regras (esquema, porta, host e, na conexão, endereço); nada de http, outra porta ou IP interno.
- **Sem proxy** (ignora `HTTP(S)_PROXY`), **sem cookie jar**, sem cabeçalhos do chamador (só `User-Agent` fixo); nenhuma credencial ou cookie segue em redirecionamento.
- **Limites:** conexão 5 s, handshake 5 s, cabeçalhos 10 s, total 20 s; corpo máximo 16 MB, **recusado (não truncado)** se passar, também quando não há `Content-Length`.
- O erro devolvido à pessoa é genérico ("URL não permitida: use um endereço https público"); o endereço interno não é revelado (fica no log).
- **ClamAV preservado:** os bytes baixados passam por `scanIngress` antes de qualquer envio à Meta ou gravação.

### Exceções justificadas

- **EX-5 Download de mídia da Meta.** A URL vem da resposta autenticada da Graph API, não de uma pessoa; o conteúdo baixado é varrido. Endurecer para um conjunto de hosts da Meta é possível, mas aumenta o risco de quebrar o recebimento de mídia se a Meta mudar de CDN; fica como melhoria.
- **EX-6 Nó "HTTP callback" da URA.** URL configurada por administrador (não por usuário final) e que pode, legitimamente, apontar para um serviço interno da empresa; só devolve texto limitado. Aplicar a política de URLs de mídia quebraria essa função. Risco: um administrador pode apontar a chamada para a rede interna (SSRF por quem já é administrador). Registrado como risco residual.

## 2. URA legada

### Auditoria

Todo áudio que toca em chamada passa por **`AudioPlayer.PlayFile`** (saudação, menu, fila/espera, ringback, transferência) e **`PlayFileLoop`** (que chama `PlayFile` a cada volta). Não há cache de áudio decodificado, **não há ffmpeg na reprodução** (ffmpeg só transcodifica no *upload*, já depois da varredura) e o TTS não passa por esse caminho. O caminho do arquivo é `AudioDir + nome` vindo da configuração do fluxo (um nome com `..` podia escapar do diretório). Antes desta rodada, nada verificava veredito na reprodução: um arquivo antigo, sem varredura, tocava.

### Proteção

- **Portão único e em memória** (`calling.AudioGate`, instalado no start do servidor): `PlayFile` só abre o arquivo se ele estiver **dentro do diretório de áudio** (sem `..`, sem subpastas) **e** com veredito `clean`. Consulta O(1) em memória: **nenhuma varredura, banco ou disco durante a chamada**.
- **Pré-verificação:** o worker lista o diretório de áudio a cada passada (até 100 por vez), registra o que não tem linha (arquivo `org_<id>_...` é da organização; o resto é do sistema) e escaneia em segundo plano. As aprovações são **reconstruídas a cada passada** (e ao fim dela), então um arquivo que vai para quarentena ou cujo veredito é retirado sai do conjunto em segundos; uploads novos entram na hora (foram varridos antes do ffmpeg).
- **Falha do scanner** ou arquivo ausente: o arquivo **não** entra no conjunto.
- **Comportamento quando o áudio não está liberado:** `PlayFile` devolve `ErrAudioNotApproved` sem abrir o arquivo; o fluxo registra o erro e **segue em silêncio naquele ponto** (como já fazia quando o arquivo faltava). A telefonia não trava e não espera varredura.
- O download do áudio no editor segue o mesmo veredito; áudio de outra organização não abre pelo nome.

**Atenção operacional:** ao implantar, áudios antigos ficam mudos até serem verificados (segundos a minutos com o ClamAV no ar). **Sem `antimalware.address` configurado nenhum áudio antigo toca** (fail-closed). Antes do deploy: ClamAV configurado e uma passada do worker; confira no painel.

## 3. Campanhas antigas

| O que | Quem controla | Proteção |
|---|---|---|
| Cópia local da mídia (`campaigns/<id>.ext`), servida na prévia | WhatC | linha `campaign_media`: só `clean` serve; legado é registrado e varrido pelo worker |
| A mesma cópia usada como mídia das mensagens de campanha (histórico) | WhatC | `ServeMedia` por mensagem (`message_media`) |
| Arquivo local usado em **reenvios** | — | **não existe**: o worker nunca lê o arquivo; envia o `header_media_id` |
| `header_media_id` (mídia hospedada na **Meta**) | **a Meta** | ver abaixo |

**Limite real:** o conteúdo que a Meta hospeda é dela; o WhatC só conhece o id. Para campanhas antigas cujo id foi enviado antes da política **não há evidência de varredura e não há como obtê-la** (não temos os bytes, a menos que a cópia local exista, caso em que ela é varrida). Não declaramos esse conteúdo varrido.

**Medidas aplicadas (sem apagar nem invalidar nada):**
1. **Entrada nova:** um id só entra em campanha ou envio de template se **este servidor** o enviou à Meta após varredura limpa (`meta_media_uploads`, por organização). Ids digitados ou vindos de fora são recusados (400).
2. **Infectado:** o worker **não envia** a campanha cuja cópia local foi encontrada infectada; cada destinatário fica `failed` com a razão.
3. **Visibilidade:** `campaigns_unverified_media` no painel de antimalware e no relatório de migração (`whatomate antimalware report`) conta as campanhas cujo id não foi enviado por este servidor.
4. **Remediação manual possível:** reenviar o arquivo pelo endpoint de mídia da campanha (varre, envia, registra e passa a ser "verificada"). Nenhuma ação automática.

## Testes

- `internal/safehttp`: endereços (IPv4/IPv6, mapeado, NAT64, 6to4, Teredo, metadados), regras de URL, **DNS rebinding** (conecta ao IP validado e não consulta uma segunda resposta; segunda resolução privada é recusada), respostas mistas, redirecionamento para nome que resolve a IP privado / IP de metadados / http / outra porta / laço, cookies e credenciais não seguem, proxy ignorado, **timeouts** (cabeçalho lento, corpo gotejando, conexão), limite de bytes (exato passa, +1 recusado, corpo em chunks), status.
- `filescan_paths_test.go`: cabeçalho por URL usa o cliente seguro, não revela o endereço, é varrido (infectado 422, sem veredito 503); **URA**: legado fechado sem custo de varredura na chamada, scanner fora = fechado, liberado após varredura em segundo plano, infectado em quarentena, caminhos fora do diretório nunca, veredito retirado sai, áudio de outra organização não abre; **campanhas**: id de fora recusado (criar campanha e envio de template), id de outra organização não vale, campanha antiga reportada e preservada; worker não envia campanha infectada. `internal/calling`: `PlayFile`/`PlayFileLoop` passam pelo portão.

## Riscos remanescentes

1. Mídia de campanhas **antigas** já hospedada na Meta: sem controle nem evidência (acima).
2. Nó **HTTP callback** da URA e demais chamadas configuradas por administrador podem alcançar a rede interna (EX-6); webhooks/custom actions já bloqueiam endereços privados.
3. Áudios de URA antigos ficam mudos até a verificação; sem ClamAV configurado, mudos.
4. O download de mídia da Meta confia na URL devolvida pela Graph API (EX-5).
5. O portão de URA tem atraso de até uma passada do worker (≈10 s) para refletir uma quarentena; o veredito do upload entra na hora.
