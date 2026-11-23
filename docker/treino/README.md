# Ambiente de treinamento (botdemo.atcpisos.com)

Uma segunda instância do WhatC **no mesmo servidor** da produção, separada dela em tudo: banco, Redis, uploads, segredos, porta e domínio. Serve a três coisas: a equipe treinar, a IA ser testada com conversas reais de teste, e **cada versão ser ensaiada antes do deploy de produção**.

| | Produção | Treinamento |
|---|---|---|
| Diretório | `/opt/whatc` | `/opt/whatc-treino` |
| Repositório | `/opt/whatc-repo` (sempre `main`) | `/opt/whatc-treino-repo` (qualquer ref) |
| Containers | `whatomate_app/db/redis` | `whatc_treino_app/db/redis` |
| Banco | `whatomate` | `whatc_treino` |
| Porta local | 8080 | 8081 |
| Domínio | o de produção | `botdemo.atcpisos.com` |
| WhatsApp | número real, app da Meta de produção | **número de teste**, app da Meta separado |
| Deploy | `deploy-whatc` | `deploy-whatc-treino` |

O treino só compartilha com a produção o **ClamAV** (scanner de arquivos, sem estado e sem dados): ele entra na rede interna da produção apenas para alcançá-lo. Remova isso do compose se preferir um scanner próprio (precisa de ~2 GB de RAM livres).

## Regras

1. **Nunca copie dados da produção para cá** (LGPD). Os dados do treino são fictícios (`scripts/treino/seed.py`).
2. **Nenhum segredo repetido.** `encryption_key` e `jwt.secret` do treino têm de ser diferentes dos de produção; o `deploy-whatc-treino` recusa subir se forem iguais.
3. Toda versão passa pelo treino antes de produção: `deploy-whatc-treino` (de `development`), a equipe confere, só então o deploy de produção.
4. Uma **faixa amarela "Ambiente de treinamento"** aparece no topo de todas as telas (vem de `environment_label` no `config.toml`).

## Instalação (uma vez, como root no servidor)

```bash
install -d -m 700 /opt/whatc-treino /opt/whatc-treino/uploads
cd /opt/whatc-treino

# 1. arquivos de exemplo do repositório de produção (ja clonado)
cp /opt/whatc-repo/docker/treino/.env.example .env
cp /opt/whatc-repo/docker/treino/config.example.toml config.toml
chmod 600 .env config.toml

# 2. gerar segredos proprios e preencher .env e config.toml
openssl rand -hex 24    # POSTGRES_PASSWORD (.env) e [database].password (config.toml)
openssl rand -hex 32    # [app].encryption_key
openssl rand -hex 32    # [jwt].secret
openssl rand -hex 16    # [whatsapp].webhook_verify_token
#    e trocar [default_admin].password

# 3. instalar o comando de deploy do treino
install -m 755 /opt/whatc-repo/docker/deploy-whatc-treino.sh /usr/local/sbin/deploy-whatc-treino
```

O `deploy-whatc-treino` clona o repositório do treino sozinho na primeira vez. Os arquivos acima só existem em `/opt/whatc-repo` depois que esta mudança chega à `main` (`git -C /opt/whatc-repo pull --ff-only origin main`, que é seguro: o deploy de produção exige essa pasta limpa e na `main`). Antes disso, ensaie direto da branch: `deploy-whatc-treino feature/ambiente-treino`.

## Domínio e proxy

O domínio `botdemo.atcpisos.com` precisa de um registro DNS (A/AAAA) para o servidor e de um bloco no proxy reverso apontando para `127.0.0.1:8081`, com TLS. Exemplo (nginx; adapte ao proxy que a produção usa):

```nginx
server {
    server_name botdemo.atcpisos.com;
    # listen 443 ssl; + certificado (certbot --nginx -d botdemo.atcpisos.com)
    client_max_body_size 50m;
    location / {
        proxy_pass http://127.0.0.1:8081;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;       # WebSocket do chat
        proxy_set_header Connection "upgrade";
    }
}
```

## WhatsApp (número de teste da Meta)

No **aplicativo da Meta do número de teste** (não no de produção): Callback URL `https://botdemo.atcpisos.com/api/webhook` e o Verify Token igual ao `webhook_verify_token` do `config.toml`. Depois, na tela do treino, cadastre a conta do WhatsApp (Phone ID, Business ID, token, app secret) em Configurações. O webhook que hoje aponta para a máquina de desenvolvimento passa a apontar para cá.

## Dados de exemplo

```bash
TREINO_URL=https://botdemo.atcpisos.com \
TREINO_ADMIN_EMAIL=admin@botdemo.local TREINO_ADMIN_PASSWORD='...' \
python3 scripts/treino/seed.py
```

Cria uma unidade, duas equipes, dois usuários (vendedor e atendente, com senha aleatória gravada em `treino-usuarios.txt`), cinco contatos fictícios e suas oportunidades. O script recusa rodar contra um endereço que não seja de treino.

## Recomeçar do zero

Para devolver o treino ao estado limpo (apaga **só** o treino):

```bash
cd /opt/whatc-treino && docker compose down -v && rm -rf uploads/* && deploy-whatc-treino
```

## O que o `deploy-whatc-treino` faz

Valida os arquivos e os segredos, atualiza o código (`development` por padrão; aceita `main`, uma branch ou `pr/<número>`), faz dump do banco do treino (guarda os 7 últimos), constrói a imagem `whatomate:treino`, sobe, espera `/ready` e, se falhar, volta à imagem anterior. Não toca em `/opt/whatc`, nos containers `whatomate_*` nem em `/opt/whatc-repo`.
