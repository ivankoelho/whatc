#!/bin/bash
set -Eeuo pipefail

#############################################
# WHATC TREINAMENTO DEPLOY
#############################################
# Sobe uma versao do codigo no ambiente de TREINAMENTO (/opt/whatc-treino), que e totalmente separado da
# producao: banco, Redis, uploads, segredos e porta proprios. Serve para a equipe treinar e para ensaiar uma
# versao ANTES do deploy de producao.
#
#   deploy-whatc-treino              # versao de origin/development (o que vai para producao)
#   deploy-whatc-treino main         # o que esta em producao hoje
#   deploy-whatc-treino feature/x    # qualquer branch, para ensaiar antes de abrir o PR
#   deploy-whatc-treino pr/173       # o PR #173 do GitHub
#
# Este script NUNCA toca em /opt/whatc, nos containers whatomate_* nem no repositorio /opt/whatc-repo.

REF="${1:-development}"

TREINO_DIR="/opt/whatc-treino"
REPO_DIR="/opt/whatc-treino-repo"
REPO_URL_FROM="/opt/whatc-repo"      # de onde clonar na primeira vez (mesmo remoto da producao)

COMPOSE_FILE="${TREINO_DIR}/docker-compose.yml"
CONTAINER_APP="whatc_treino_app"
CONTAINER_DB="whatc_treino_db"
APP_IMAGE="whatomate:treino"

BACKUP_DIR="${TREINO_DIR}/backups"
BACKUP_KEEP=7

TIMESTAMP=$(date +"%Y%m%d-%H%M%S")
ROLLBACK_IMAGE="whatomate:treino-rollback-${TIMESTAMP}"

log()  { echo; echo "[TREINO-DEPLOY] $1"; }
fail() { echo; echo "DEPLOY DO TREINO ABORTADO: $1"; exit 1; }

#############################################
# Validacao
#############################################

log "Validando estrutura"

[ "$(id -u)" -eq 0 ] || fail "Execute como root."
[ -d "$TREINO_DIR" ] || fail "Diretorio nao encontrado: $TREINO_DIR (veja docker/treino/README.md)"
[ -f "$TREINO_DIR/.env" ] || fail "Falta $TREINO_DIR/.env (copie de docker/treino/.env.example)."
[ -f "$TREINO_DIR/config.toml" ] || fail "Falta $TREINO_DIR/config.toml (copie de docker/treino/config.example.toml)."
mkdir -p "$TREINO_DIR/uploads" "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

# Trava de seguranca: o treino nunca pode compartilhar segredo nem banco com a producao.
PROD_CFG="/opt/whatc/config/config.toml"
if [ -f "$PROD_CFG" ]; then
    for key in encryption_key secret; do
        T=$(grep -E "^${key}[[:space:]]*=" "$TREINO_DIR/config.toml" | head -1 | sed 's/^[^=]*=[[:space:]]*//; s/[[:space:]]*#.*//' || true)
        P=$(grep -E "^${key}[[:space:]]*=" "$PROD_CFG" | head -1 | sed 's/^[^=]*=[[:space:]]*//; s/[[:space:]]*#.*//' || true)
        [ -z "$T" ] || [ "$T" != "$P" ] || fail "'${key}' do treino e IGUAL ao da producao. Gere um proprio."
    done
fi
grep -q 'TROQUE\|GERE-' "$TREINO_DIR/config.toml" "$TREINO_DIR/.env" \
    && fail "Ainda ha valores de exemplo (TROQUE/GERE) em config.toml ou .env."
grep -q '^environment_label' "$TREINO_DIR/config.toml" \
    || fail "config.toml sem environment_label: a faixa de treinamento nao apareceria nas telas."

#############################################
# Codigo
#############################################

log "Atualizando o codigo ($REF)"

if [ ! -d "$REPO_DIR/.git" ]; then
    [ -d "$REPO_URL_FROM/.git" ] || fail "Repositorio de origem nao encontrado: $REPO_URL_FROM"
    ORIGIN=$(git -C "$REPO_URL_FROM" remote get-url origin)
    git clone "$ORIGIN" "$REPO_DIR"
fi

cd "$REPO_DIR"
git fetch origin --prune
git reset -q --hard
git clean -qfd

case "$REF" in
    pr/*) N="${REF#pr/}"; git fetch origin "pull/${N}/head:treino-pr-${N}" --force; TARGET="treino-pr-${N}" ;;
    *)    TARGET="origin/${REF}" ;;
esac
git rev-parse --verify -q "$TARGET" >/dev/null || fail "Referencia nao encontrada: $REF"
git checkout -q --detach "$TARGET"
COMMIT=$(git rev-parse --short HEAD)
echo "Commit no treino: $COMMIT ($REF)"

[ -f "$REPO_DIR/docker/treino/docker-compose.yml" ] || fail "A versao $COMMIT ($REF) nao tem docker/treino (ainda nao inclui o ambiente de treinamento)."
cp "$REPO_DIR/docker/treino/docker-compose.yml" "$COMPOSE_FILE"
cd "$TREINO_DIR"
docker compose config -q || fail "Compose do treino invalido."

#############################################
# Backup do banco do treino (barato; a producao nao e tocada)
#############################################

if docker ps --format '{{.Names}}' | grep -qx "$CONTAINER_DB"; then
    log "Backup do banco do treino"
    # shellcheck disable=SC1091
    set -a; . "$TREINO_DIR/.env"; set +a
    docker exec "$CONTAINER_DB" pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc > "$BACKUP_DIR/treino-${TIMESTAMP}.dump" \
        || fail "Falha no dump do banco do treino."
    chmod 600 "$BACKUP_DIR/treino-${TIMESTAMP}.dump"
    ls -1t "$BACKUP_DIR"/treino-*.dump | tail -n +$((BACKUP_KEEP + 1)) | xargs -r rm -f
fi

#############################################
# Build e subida
#############################################

log "Construindo a imagem"

if docker image inspect "$APP_IMAGE" >/dev/null 2>&1; then
    docker tag "$APP_IMAGE" "$ROLLBACK_IMAGE"
fi
# Poda: mantem so as KEEP_ROLLBACK (padrao 1) tags de rollback mais recentes.
KEEP_ROLLBACK="${KEEP_ROLLBACK:-1}"
docker image ls --format '{{.Repository}}:{{.Tag}}' whatomate \
    | grep -E '^whatomate:treino-rollback-[0-9]{8}-[0-9]{6}$' | sort -r | tail -n +"$((KEEP_ROLLBACK + 1))" \
    | xargs -r docker image rm >/dev/null 2>&1 || true
docker compose build app || fail "Falha no build."

log "Subindo o treino"
docker compose up -d || fail "Falha ao subir o treino."

#############################################
# Saude (com rollback)
#############################################

# shellcheck disable=SC1091
set -a; . "$TREINO_DIR/.env"; set +a
HEALTH_URL="http://127.0.0.1:${TREINO_PORT:-8081}/ready"

READY=0
for _ in $(seq 1 60); do
    S=$(curl -sS -o /dev/null -w "%{http_code}" "$HEALTH_URL" 2>/dev/null || true)
    if [ "$S" = "200" ]; then READY=1; break; fi
    sleep 5
done

if [ "$READY" -ne 1 ]; then
    echo "O treino nao ficou saudavel. Ultimas linhas do log:"
    docker logs --tail 40 "$CONTAINER_APP" 2>&1 || true
    if docker image inspect "$ROLLBACK_IMAGE" >/dev/null 2>&1; then
        echo "Revertendo para a imagem anterior..."
        docker tag "$ROLLBACK_IMAGE" "$APP_IMAGE"
        docker compose up -d app || true
    fi
    fail "A versao $COMMIT ($REF) nao subiu. A producao nao foi afetada."
fi

log "Treino no ar"
echo "Versao   : $COMMIT ($REF)"
echo "Saude    : $HEALTH_URL = 200"
echo "Producao : intocada"
