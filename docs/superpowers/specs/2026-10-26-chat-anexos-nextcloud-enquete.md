# Chat interno: menu Anexar (computador, Nextcloud, enquete) e login centralizado

## Menu Anexar
O clipe do compositor abre um menu: **Arquivo neste computador** (fluxo de upload de sempre), **Arquivo no Nextcloud** e **Enquete**.

## Enquete (simples)
- Uma enquete é uma mensagem do canal: `Body` = pergunta, `Meta.poll` = `{multiple, options[{id,label}]}`; votos em `chat_poll_votes` (um por pessoa e opção).
- `POST /api/team-chat/channels/{id}/polls` (pergunta 1–300, 2 a 10 opções distintas de até 100 caracteres, voto único ou múltiplo) e `PUT /api/team-chat/messages/{id}/poll-vote` (substitui o voto de quem vota; lista vazia retira o voto).
- Acesso, arquivamento e exclusão seguem as regras de qualquer mensagem; votar exige `team_chat:write`, acesso ao canal e canal não arquivado. Enquete não pode ser editada. Tempo real pelo mesmo evento das reações (o cliente recarrega a mensagem).
- Fora do escopo: prazo e voto anônimo.

## Nextcloud
- Um servidor por organização (`nextcloud_settings`), cadastrado por quem tem `settings.general:write`; cada pessoa se conecta com **usuário + senha de aplicativo** (revogável no Nextcloud), guardada criptografada em `nextcloud_credentials` e nunca devolvida ao navegador. Trocar o endereço do servidor apaga as credenciais.
- `GET/PUT /api/integrations/nextcloud`, `PUT/DELETE /api/integrations/nextcloud/me`, `GET /api/integrations/nextcloud/files?path=`, `POST /api/team-chat/channels/{id}/nextcloud-files`.
- O servidor busca o arquivo por WebDAV e o entrega ao MESMO caminho de upload do computador (tipo, tamanho e acesso ao canal idênticos). Caminhos com `..` são recusados; redirecionamentos para outro host não são seguidos.
- Endereço: somente `https` público. `app.allow_private_nextcloud = true` (ou `WHATOMATE_APP__ALLOW_PRIVATE_NEXTCLOUD=true`) libera http e endereços privados, **somente para desenvolvimento**; sem isso um administrador poderia apontar o servidor para a rede interna (SSRF).
- Limites atuais: lista até 500 itens por pasta; arquivo até o limite de anexos do chat; sem navegação de arquivos compartilhados ou links de compartilhamento.

## Login
Página única centralizada: logo e nome do sistema pequenos, e-mail, senha, botão, ícones de login social em uma linha e link de cadastro. Sem frase de efeito à esquerda nem blocos de texto grandes.

## Chat Interno centralizado (Fase B.1)
- As discussões de protocolos aparecem no módulo Chat Interno, seção **Chamados**: as de que a pessoa participa (responsável, quem abriu, participante, quem escreveu ou leu) e que já têm mensagem, até 100, com contagem de não lidas pelo mesmo mecanismo dos demais canais (marcador de leitura por canal).
- **Busca** (`GET /api/team-chat/occurrence-search?q=`, mínimo de 2 caracteres): protocolo, título ou nome do cliente, entre os chamados que a pessoa pode ver (mesmo escopo da lista de chamados: `visibleOccurrences`); a caixa de busca da barra lateral também filtra canais e conversas já listados. Abrir um resultado aplica o acesso de sempre (acesso ao chamado + `occurrences:read`); ao ler, a discussão passa a fazer parte da lista da pessoa.
- **Navegação nos dois sentidos**: "Abrir chamado" no cabeçalho da conversa; "Abrir no Chat Interno" no cartão de discussão do chamado.
- Notificação/ não lidas: eventos das discussões já chegam só a quem pode ver o chamado; o badge soma os não lidos da lista.

## Documentos do protocolo (B.2)
- Tabela `occurrence_files` (soft delete; o arquivo salvo é apagado na remoção). Rotas: `GET/POST /api/occurrences/{id}/files`, `POST .../files/nextcloud`, `GET .../files/{fid}/download[?inline=1]`, `DELETE .../files/{fid}`.
- Tipos pelo conteúdo (nunca pela extensão): PDF, JPG/PNG/WEBP/GIF, DOCX/XLSX/PPTX, DOC/XLS (OLE), TXT/CSV e XML sem DTD/entidades. Recusados: executáveis, scripts, HTML/SVG, Office com macro, áudio, extensão que não bate com o conteúdo. Limite configurável `app.occurrence_file_max_mb` (padrão 15).
- Quem pode: **listar** quem abre o chamado; **baixar** quem trabalha o chamado, quem enviou e, se o arquivo não for "restrito", quem abre o chamado (padrão: restrito); **anexar** quem trabalha o chamado ou participante com direito de comentar (e `occurrences:write`); **remover** quem enviou ou supervisor do chamado. Nextcloud: o servidor busca com as credenciais de quem pediu (ninguém puxa arquivos de outra pessoa).
- Nome interno aleatório (`occurrence-files/<org>/<chamado>/<uuid>.<ext>`), nada do nome do usuário no caminho; download com `nosniff`, CSP `sandbox`, `no-store`; só PDF/imagem abrem inline. Reenvio idêntico em 30 s não duplica. Envio/remoção viram eventos do histórico; envio, download e remoção entram na auditoria. Retenção: o arquivo acompanha o chamado (sem expurgo automático por prazo ainda).
- Sem antivírus no servidor: a verificação contra malware depende de integrar um scanner (pendência).

## Leitura de notas fiscais (B.3)
- `POST /api/occurrences/{id}/documents/extract` propõe valores; **não salva nada** e exige poder criar notas no chamado. XML de NF-e: número, série, data, chave (dígito verificador conferido), emitente e produtos. PDF com texto: chave de acesso → número e série, e a data quando achada; produtos não são lidos de PDF. Imagem/PDF digitalizado: exige OCR, que **não está instalado**: a tela diz isso e o formulário segue manual. PDF usa `pdftotext` (poppler-utils) se existir no servidor.
- O formulário só preenche campos vazios (nunca sobrescreve), mostra o que foi lido, o que é incerto e avisos, e o usuário confirma com Salvar. `source` e `access_key` ficam gravados no documento. Não é validação fiscal de autenticidade.

## Histórico completo (B.4)
- `GET /api/occurrences/{id}/history?limit&cursor&types&q`: do mais novo ao mais antigo, cursor por (data, id) estável com eventos novos, filtros, busca. O texto das mensagens ao cliente (`reply`, `process_message_used`) só aparece a quem pode ler a conversa; os demais veem que houve a mensagem (e a busca não encontra o texto). O endpoint antigo da linha do tempo segue a mesma regra. A mudança de etapa que é só a outra metade de "concluir/reabrir" (mesma pessoa, mesmo instante) não é listada duas vezes.

## Exportação (B.5)
- `POST /api/occurrences/{id}/export` e `GET .../export/options`. Seções: resumo, histórico, discussão interna, WhatsApp, documentos, "tudo" (= o que a pessoa pode, listando o que ficou de fora). Seção sem permissão pedida explicitamente → 403. WhatsApp só para quem pode ler a conversa; discussão interna para quem lê o canal do chamado; arquivos do ZIP só os que a pessoa pode baixar. Formatos: PDF (gerador próprio, sem dependência), CSV (uma tabela; células com `=+-@` neutralizadas) e ZIP (relatório, CSVs e documentos). Montado em memória, sem arquivo temporário; auditoria registra seções, formato e contagens. Limites: 5.000 eventos/mensagens e 100 MB de arquivos por exportação (acima → 413; exportação assíncrona e XLSX ficam como pendência).
