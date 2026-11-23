#!/usr/bin/env python3
"""Popula o ambiente de TREINAMENTO com dados FICTICIOS (nunca copie dados reais para ca: LGPD).

Usa a API publica do sistema, como um administrador faria na tela. Idempotente de forma simples: se o contato
ou a unidade ja existe, segue em frente.

  TREINO_URL=https://botdemo.atcpisos.com \
  TREINO_ADMIN_EMAIL=... TREINO_ADMIN_PASSWORD=... \
  python3 scripts/treino/seed.py

As credenciais vem do ambiente, nunca do arquivo. Os usuarios criados recebem senhas aleatorias, gravadas em
./treino-usuarios.txt (chmod 600): entregue-as a cada pessoa e apague o arquivo.
"""
import http.cookiejar
import json
import os
import secrets
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("TREINO_URL", "").rstrip("/") + "/api"
EMAIL = os.environ.get("TREINO_ADMIN_EMAIL", "")
PASSWORD = os.environ.get("TREINO_ADMIN_PASSWORD", "")
if not os.environ.get("TREINO_URL") or not EMAIL or not PASSWORD:
    sys.exit("Defina TREINO_URL, TREINO_ADMIN_EMAIL e TREINO_ADMIN_PASSWORD.")
if "botdemo" not in BASE and "localhost" not in BASE and "127.0.0.1" not in BASE:
    sys.exit(f"Recusado: {BASE} nao parece o ambiente de treinamento (este script so popula dados ficticios).")

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def call(method, path, body=None):
    csrf = next((c.value for c in jar if c.name == "whm_csrf"), "")
    req = urllib.request.Request(
        BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json", "X-CSRF-Token": csrf},
    )
    try:
        with opener.open(req) as r:
            return r.status, json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"{}")
        except ValueError:
            return e.code, {}


status, _ = call("POST", "/auth/login", {"email": EMAIL, "password": PASSWORD})
if status != 200:
    sys.exit(f"Login recusado ({status}).")

# --- unidade, equipes e usuarios ---------------------------------------------------------------------------
s, r = call("POST", "/units", {"name": "Loja Treinamento", "type": "store", "active": True})
unit = r.get("data", {}).get("id")
print("unidade", s)

team_ids = {}
for name in ("Vendas Treinamento", "Atendimento Treinamento"):
    s, r = call("POST", "/teams", {"name": name, "assignment_strategy": "manual", "unit_id": unit})
    team_ids[name] = r.get("data", {}).get("team", {}).get("id")
    print("equipe", name, s)

roles = {x["name"]: x["id"] for x in call("GET", "/roles")[1].get("data", {}).get("roles", [])}
creds = []
for email, name, role, team in (
    ("vendedor.treino@botdemo.local", "Vendedor Treinamento", "agent", "Vendas Treinamento"),
    ("atendente.treino@botdemo.local", "Atendente Treinamento", "agent", "Atendimento Treinamento"),
):
    if role not in roles:
        print("papel ausente:", role)
        continue
    pw = secrets.token_urlsafe(12)
    s, r = call("POST", "/users", {"email": email, "password": pw, "full_name": name, "role_id": roles[role]})
    print("usuario", email, s)
    if s in (200, 201):
        creds.append(f"{email}  {pw}")
        if team_ids.get(team):
            call("POST", f"/teams/{team_ids[team]}/members", {"user_id": r["data"]["id"], "role": "agent"})
if creds:
    path = "treino-usuarios.txt"
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(creds) + "\n")
    os.chmod(path, 0o600)
    print("senhas em", path)

# --- contatos e oportunidades ficticios -------------------------------------------------------------------
# Telefones 55 71 9 0000-00NN: faixa de exemplo, nao pertence a ninguem.
people = [
    ("Maria Exemplo", "porcelanato 60x60 para sala, cerca de 40 m2", 6800.0, "potencial"),
    ("Joao de Teste", "piso laminado para quarto", 2400.0, "abrir_orcamento"),
    ("Construtora Modelo Ltda", "revestimento para fachada, obra de 300 m2", 31500.0, "abrir_orcamento"),
    ("Ana Fulana", "argamassa e rejunte para banheiro", 520.0, "potencial"),
    ("Carlos Ficticio", "piso vinilico para loja", 9800.0, "potencial"),
]
for i, (name, interest, value, stage) in enumerate(people, start=1):
    phone = f"557190000{i:04d}"
    s, r = call("POST", "/contacts", {"phone_number": phone, "profile_name": name, "contact_type": "cliente"})
    contact = r.get("data", {}).get("id") or r.get("data", {}).get("contact", {}).get("id")
    if not contact:
        print("contato", name, s, "(ja existe ou recusado)")
        continue
    s, r = call("POST", "/sales-opportunities", {"contact_id": contact, "interest": interest, "estimated_value": value})
    opp = r.get("data", {}).get("id")
    print("oportunidade", name, s)
    if opp and stage != "potencial":
        call("PUT", f"/sales-opportunities/{opp}/stage", {"stage": stage})
    if opp and i == 1:
        call("PUT", f"/sales-opportunities/{opp}/details", {"next_task_at": "2030-01-15T13:00:00Z", "next_task_note": "Ligar com o orcamento"})

print("pronto. Falta, na tela: a conta do WhatsApp do numero de teste e o agente de IA do treinamento.")
