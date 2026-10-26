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
