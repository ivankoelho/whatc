#!/bin/bash
set -Eeuo pipefail

#############################################
# WHATC PRODUCTION DEPLOY
#############################################

APP_NAME="whatomate"

REPO_DIR="/opt/whatc-repo"
PROD_DIR="/opt/whatc"

COMPOSE_FILE="${PROD_DIR}/docker-compose.yml"

CONTAINER_APP="whatomate_app"
CONTAINER_DB="whatomate_db"
CONTAINER_REDIS="whatomate_redis"

DOCKER_NETWORK="docker_whatomate"
POSTGRES_VOLUME="docker_postgres-data"
REDIS_VOLUME="docker_redis-data"

PROD_BRANCH="main"

APP_IMAGE="whatomate:fork"

# /ready faz Ping() no banco. A raiz "/" devolve o SPA estatico e responde 200
# mesmo com a aplicacao quebrada, entao nao serve como health check.
HEALTH_URL="http://127.0.0.1:8080/ready"

BACKUP_DIR="${PROD_DIR}/backups"
BACKUP_RETENTION_DAYS=30

TIMESTAMP=$(date +"%Y%m%d-%H%M%S")

ROLLBACK_IMAGE="whatomate:rollback-${TIMESTAMP}"

#############################################
# Helpers
#############################################

log() {
    echo
    echo "============================================================"
    echo "[WHATC-DEPLOY] $1"
    echo "============================================================"
}

fail() {
    DEPLOY_FAILED=1
    echo
    echo "============================================================"
    echo "DEPLOY ABORTADO"
    echo "============================================================"
    echo "$1"
    echo "============================================================"
    exit 1
}

# Volta a imagem anterior e sobe de novo. Usada quando a aplicacao nova nao
# fica saudavel: sem isso o deploy morre com producao ja no ar quebrada.
rollback() {
    if ! docker image inspect "$ROLLBACK_IMAGE" >/dev/null 2>&1; then
        echo "[WHATC-DEPLOY] Sem imagem de rollback ($ROLLBACK_IMAGE)."
        echo "[WHATC-DEPLOY] Rollback automatico NAO e possivel."
        return 1
    fi

    echo
    echo "[WHATC-DEPLOY] Revertendo para a imagem anterior..."

    docker tag "$ROLLBACK_IMAGE" "$APP_IMAGE"

    ( cd "$PROD_DIR" && docker compose up -d app ) || {
        echo "[WHATC-DEPLOY] Falha ao subir a imagem anterior."
        return 1
    }

    for i in $(seq 1 30); do
        local status
        status=$(curl -sS -o /dev/null -w "%{http_code}" "$HEALTH_URL" 2>/dev/null || true)
        if [ "$status" = "200" ]; then
            echo "[WHATC-DEPLOY] Rollback concluido. Producao restaurada."
            return 0
        fi
        sleep 5
    done

    echo "[WHATC-DEPLOY] Rollback executado mas a aplicacao nao respondeu."
    return 1
}

cleanup() {
    if [ "${DEPLOY_FAILED:-0}" -eq 1 ]; then
        echo
        echo "[WHATC-DEPLOY] Deploy falhou."
        echo "[WHATC-DEPLOY] Banco e volumes NAO foram removidos."
        echo "[WHATC-DEPLOY] Producao permanece preservada."
    fi
}

trap cleanup EXIT

#############################################
# Validacao basica
#############################################

log "Validando estrutura"

[ "$(id -u)" -eq 0 ] || fail "Execute como root."

[ -d "$REPO_DIR" ] || fail "Repositorio nao encontrado: $REPO_DIR"

[ -d "$PROD_DIR" ] || fail "Diretorio de producao nao encontrado: $PROD_DIR"

[ -f "$COMPOSE_FILE" ] || fail "Compose nao encontrado: $COMPOSE_FILE"

[ -f "$PROD_DIR/.env" ] || fail "Arquivo .env nao encontrado."

[ -f "$PROD_DIR/config/config.toml" ] || fail "Configuracao nao encontrada."

[ -d "$PROD_DIR/uploads" ] || fail "Diretorio uploads nao encontrado."

#############################################
# Git
#############################################

log "Validando repositorio Git"

cd "$REPO_DIR"

git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || fail "Diretorio nao e um repositorio Git valido."

CURRENT_BRANCH=$(git branch --show-current)

[ "$CURRENT_BRANCH" = "$PROD_BRANCH" ] \
    || fail "Branch atual e '$CURRENT_BRANCH'. Producao exige '$PROD_BRANCH'."

# --porcelain cobre modificados, staged E nao rastreados de uma vez.
GIT_DIRT=$(git status --porcelain)

[ -z "$GIT_DIRT" ] \
    || fail "Repositorio sujo (alteracoes locais ou arquivos nao rastreados):
$GIT_DIRT"

#############################################
# Docker
#############################################

log "Validando recursos Docker"

docker network inspect "$DOCKER_NETWORK" >/dev/null 2>&1 \
    || fail "Network externa nao encontrada: $DOCKER_NETWORK"

docker volume inspect "$POSTGRES_VOLUME" >/dev/null 2>&1 \
    || fail "Volume PostgreSQL nao encontrado: $POSTGRES_VOLUME"

docker volume inspect "$REDIS_VOLUME" >/dev/null 2>&1 \
    || fail "Volume Redis nao encontrado: $REDIS_VOLUME"

#############################################
# Compose
#############################################

log "Validando Compose de producao"

cd "$PROD_DIR"

docker compose config -q \
    || fail "Compose de producao invalido."

echo "Compose valido."

#############################################
# Producao atual
#############################################

log "Validando producao atual"

docker ps --format '{{.Names}}' | grep -qx "$CONTAINER_DB" \
    || fail "Container PostgreSQL nao esta em execucao."

docker ps --format '{{.Names}}' | grep -qx "$CONTAINER_REDIS" \
    || fail "Container Redis nao esta em execucao."

docker ps --format '{{.Names}}' | grep -qx "$CONTAINER_APP" \
    || fail "Container Whatomate nao esta em execucao."

#############################################
# Backup de seguranca
#############################################

log "Criando backup de seguranca"

mkdir -p "$BACKUP_DIR"

chmod 700 "$BACKUP_DIR"

cp "$COMPOSE_FILE" \
    "$BACKUP_DIR/docker-compose-${TIMESTAMP}.yml"

cp "$PROD_DIR/.env" \
    "$BACKUP_DIR/env-${TIMESTAMP}.bak"

chmod 600 \
    "$BACKUP_DIR/env-${TIMESTAMP}.bak"

#############################################
# Estado antes
#############################################

log "Registrando estado atual"

count_rows() {
    docker exec "$CONTAINER_DB" \
        psql -U whatomate -d whatomate -tAc \
        "SELECT COUNT(*) FROM $1;"
}

USERS_BEFORE=$(count_rows users)
CONTACTS_BEFORE=$(count_rows contacts)
MESSAGES_BEFORE=$(count_rows messages)

for v in "$USERS_BEFORE" "$CONTACTS_BEFORE" "$MESSAGES_BEFORE"; do
    [[ "$v" =~ ^[0-9]+$ ]] || fail "Contagem invalida vinda do banco: '$v'"
done

echo "Usuarios : $USERS_BEFORE"
echo "Contatos : $CONTACTS_BEFORE"
echo "Mensagens: $MESSAGES_BEFORE"

#############################################
# Backup PostgreSQL
#############################################

log "Criando dump PostgreSQL"

DUMP_FILE="$BACKUP_DIR/postgres-${TIMESTAMP}.dump"

docker exec "$CONTAINER_DB" \
    pg_dump -U whatomate -d whatomate -Fc \
    > "$DUMP_FILE"

[ -s "$DUMP_FILE" ] \
    || fail "Dump PostgreSQL vazio ou invalido."

# -s so garante "nao vazio"; um dump truncado passaria. --list le o indice do
# arquivo e falha se estiver corrompido.
pg_restore --list "$DUMP_FILE" >/dev/null 2>&1 \
    || docker exec -i "$CONTAINER_DB" pg_restore --list < "$DUMP_FILE" >/dev/null 2>&1 \
    || fail "Dump PostgreSQL corrompido (pg_restore --list falhou)."

chmod 600 "$DUMP_FILE"

echo "Dump criado e validado:"
ls -lh "$DUMP_FILE"

#############################################
# Atualizacao do Git
#############################################

log "Atualizando codigo da producao"

cd "$REPO_DIR"

git fetch origin

git pull --ff-only origin "$PROD_BRANCH" \
    || fail "Falha ao atualizar codigo via Git."

NEW_COMMIT=$(git rev-parse --short HEAD)

echo "Commit em producao: $NEW_COMMIT"

#############################################
# Dependencias (provisionadas a cada deploy)
#############################################
# O gate de uploads e fail-closed: sem scanner (ClamAV) todo upload e midia fica
# bloqueado. Por isso o scanner e uma dependencia do app, subida e conferida aqui,
# e nao um passo manual que alguem pode esquecer numa instalacao nova.

log "Garantindo dependencias (ClamAV / antimalware)"

CLAMAV_CONTAINER="whatomate_clamav"
OVERRIDE_SRC="${REPO_DIR}/docker/docker-compose.antimalware.yml"
OVERRIDE_DST="${PROD_DIR}/docker-compose.override.yml"

[ -f "$OVERRIDE_SRC" ] \
    || fail "Definicao do antimalware nao encontrada no repositorio: $OVERRIDE_SRC"

# Nao sobrescrever um override feito a mao.
if [ -f "$OVERRIDE_DST" ] && ! grep -q '^# managed-by: deploy-whatc' "$OVERRIDE_DST"; then
    fail "$OVERRIDE_DST existe e nao e gerenciado pelo deploy. Mova seu conteudo para o compose principal ou remova-o."
fi

# Antes de qualquer mudanca: o ClamAV carrega ~1,5 GB de assinaturas em RAM.
if ! docker ps -a --format '{{.Names}}' | grep -qx "$CLAMAV_CONTAINER"; then
    MEM_AVAIL_MB=$(awk '/MemAvailable/ {print int($2/1024)}' /proc/meminfo)
    [ "${MEM_AVAIL_MB:-0}" -ge 2048 ] \
        || fail "Memoria disponivel insuficiente para o ClamAV (precisa de ~2 GB livres, ha ${MEM_AVAIL_MB:-?} MB)."
fi

[ -f "$OVERRIDE_DST" ] && cp "$OVERRIDE_DST" "$BACKUP_DIR/docker-compose.override-${TIMESTAMP}.yml"
cp "$OVERRIDE_SRC" "$OVERRIDE_DST"

if ! ( cd "$PROD_DIR" && docker compose config -q ); then
    if [ -f "$BACKUP_DIR/docker-compose.override-${TIMESTAMP}.yml" ]; then
        cp "$BACKUP_DIR/docker-compose.override-${TIMESTAMP}.yml" "$OVERRIDE_DST"
    else
        rm -f "$OVERRIDE_DST"
    fi
    fail "Compose invalido com o antimalware (a rede 'whatomate' existe no compose de producao?)."
fi

# Instala so se faltar; atualiza so se a versao pedida no repositorio mudou.
# As assinaturas ficam no volume clamav-db, entao atualizar a imagem nao baixa tudo de novo.
WANT_IMAGE=$( cd "$PROD_DIR" && docker compose config --images | grep -m1 clamav ) \
    || fail "Imagem do ClamAV nao encontrada no compose."
HAVE_IMAGE=$(docker inspect --format '{{.Config.Image}}' "$CLAMAV_CONTAINER" 2>/dev/null || true)
CLAMAV_RUNNING=$(docker inspect --format '{{.State.Running}}' "$CLAMAV_CONTAINER" 2>/dev/null || true)

if [ -z "$HAVE_IMAGE" ]; then
    echo "ClamAV nao instalado: instalando $WANT_IMAGE."
    ( cd "$PROD_DIR" && docker compose pull clamav && docker compose up -d clamav ) \
        || fail "Falha ao instalar o ClamAV."
elif [ "$HAVE_IMAGE" != "$WANT_IMAGE" ]; then
    echo "ClamAV $HAVE_IMAGE -> $WANT_IMAGE: atualizando."
    ( cd "$PROD_DIR" && docker compose pull clamav && docker compose up -d clamav ) \
        || fail "Falha ao atualizar o ClamAV."
elif [ "$CLAMAV_RUNNING" != "true" ]; then
    echo "ClamAV $HAVE_IMAGE ja instalado, mas parado: iniciando."
    ( cd "$PROD_DIR" && docker compose up -d clamav ) \
        || fail "Falha ao iniciar o ClamAV."
else
    echo "ClamAV $HAVE_IMAGE ja instalado e em execucao: nada a fazer."
fi

# Na primeira vez ele baixa as assinaturas (minutos). Nao bloqueia o deploy: a
# aplicacao sobe e os uploads ficam bloqueados ate o scanner responder.
echo "Aguardando o ClamAV ficar saudavel (ate 10 min na primeira vez)..."
CLAMAV_OK=0
for i in $(seq 1 60); do
    H=$(docker inspect --format '{{.State.Health.Status}}' "$CLAMAV_CONTAINER" 2>/dev/null || true)
    if [ "$H" = "healthy" ]; then CLAMAV_OK=1; break; fi
    sleep 10
done
if [ "$CLAMAV_OK" -eq 1 ]; then
    echo "ClamAV saudavel."
else
    echo "ATENCAO: ClamAV ainda nao esta saudavel (status: ${H:-desconhecido})."
    echo "Uploads continuam bloqueados ate ele responder: docker logs $CLAMAV_CONTAINER"
fi

#############################################
# Build
#############################################

log "Construindo nova imagem"

cd "$PROD_DIR"

# O build sobrescreve a tag $APP_IMAGE e a imagem antiga vira dangling --
# recuperavel por ID, nao por tag. Guardar a tag agora e o que torna o
# rollback uma linha em vez de uma arqueologia.
if docker image inspect "$APP_IMAGE" >/dev/null 2>&1; then
    docker tag "$APP_IMAGE" "$ROLLBACK_IMAGE"
    echo "Imagem atual preservada como: $ROLLBACK_IMAGE"
else
    echo "Nenhuma imagem anterior encontrada; sem ponto de rollback."
fi

docker compose build app \
    || fail "Falha no build da aplicacao."

#############################################
# Deploy
#############################################

log "Atualizando aplicacao"

docker compose up -d app \
    || fail "Falha ao iniciar nova aplicacao."

#############################################
# Aguardar aplicacao
#############################################

log "Aguardando aplicacao"

READY=0

for i in $(seq 1 60); do

    STATUS=$(curl -sS \
        -o /dev/null \
        -w "%{http_code}" \
        "$HEALTH_URL" 2>/dev/null || true)

    if [ "$STATUS" = "200" ]; then
        READY=1
        echo "Aplicacao respondeu 200 em $HEALTH_URL (tentativa $i)."
        break
    fi

    echo "Tentativa $i/60 - HTTP $STATUS"

    sleep 5

done

if [ "$READY" -ne 1 ]; then

    DEPLOY_FAILED=1

    echo
    echo "Logs da aplicacao:"
    docker logs --tail=200 "$CONTAINER_APP" || true

    rollback || true

    fail "Aplicacao nao respondeu em $HEALTH_URL"

fi

#############################################
# Antimalware ativo?
#############################################

log "Conferindo o antimalware"

if docker logs --since 5m "$CONTAINER_APP" 2>&1 | grep -q "Antimalware is NOT configured"; then
    fail "O app subiu SEM scanner configurado: todo upload ficaria bloqueado."
fi

echo "App configurado para o scanner; ClamAV: $(docker inspect --format '{{.State.Health.Status}}' "$CLAMAV_CONTAINER" 2>/dev/null || echo desconhecido)"

#############################################
# Estado depois
#############################################

log "Validando dados apos deploy"

USERS_AFTER=$(count_rows users)
CONTACTS_AFTER=$(count_rows contacts)
MESSAGES_AFTER=$(count_rows messages)

for v in "$USERS_AFTER" "$CONTACTS_AFTER" "$MESSAGES_AFTER"; do
    [[ "$v" =~ ^[0-9]+$ ]] || fail "Contagem invalida vinda do banco: '$v'"
done

echo "Usuarios  antes/depois: $USERS_BEFORE / $USERS_AFTER"
echo "Contatos  antes/depois: $CONTACTS_BEFORE / $CONTACTS_AFTER"
echo "Mensagens antes/depois: $MESSAGES_BEFORE / $MESSAGES_AFTER"

# Producao esta viva e recebe WhatsApp durante os minutos de build, entao as
# contagens SOBEM naturalmente. Exigir igualdade transformaria uma mensagem
# recebida no meio do deploy em falha. O que importa e que nada sumiu.
[ "$USERS_AFTER" -ge "$USERS_BEFORE" ] \
    || fail "Usuarios diminuiram: $USERS_BEFORE -> $USERS_AFTER"

[ "$CONTACTS_AFTER" -ge "$CONTACTS_BEFORE" ] \
    || fail "Contatos diminuiram: $CONTACTS_BEFORE -> $CONTACTS_AFTER"

[ "$MESSAGES_AFTER" -ge "$MESSAGES_BEFORE" ] \
    || fail "Mensagens diminuiram: $MESSAGES_BEFORE -> $MESSAGES_AFTER"

#############################################
# Retencao de backups
#############################################

log "Limpando backups antigos"

find "$BACKUP_DIR" -maxdepth 1 -type f \
    \( -name 'postgres-*.dump' -o -name 'docker-compose-*.yml' -o -name 'env-*.bak' \) \
    -mtime "+${BACKUP_RETENTION_DAYS}" -print -delete || true

echo "Mantendo backups dos ultimos ${BACKUP_RETENTION_DAYS} dias."

#############################################
# Status final
#############################################

log "Deploy concluido"

echo "Commit:"
git -C "$REPO_DIR" log -1 --oneline

echo
echo "Containers WhatC:"

docker ps \
    --filter "name=whatomate" \
    --format "table {{.Names}}\t{{.Status}}\t{{.Image}}"

echo
echo "Aplicacao:"

curl -sS \
    -o /dev/null \
    -w "HTTP %{http_code}\n" \
    "$HEALTH_URL"

echo
echo "Imagem atual:"

docker inspect "$CONTAINER_APP" \
    --format '{{.Config.Image}}'

echo
echo "Ponto de rollback:"
if docker image inspect "$ROLLBACK_IMAGE" >/dev/null 2>&1; then
    echo "  $ROLLBACK_IMAGE"
    echo "  Para reverter:"
    echo "    docker tag $ROLLBACK_IMAGE $APP_IMAGE && cd $PROD_DIR && docker compose up -d app"
else
    echo "  (nenhum)"
fi

echo
echo "Backup criado:"

ls -lh "$BACKUP_DIR"/*"$TIMESTAMP"* 2>/dev/null || true

echo
echo "============================================================"
echo " WHATC PRODUCAO ATUALIZADO COM SUCESSO "
echo "============================================================"
