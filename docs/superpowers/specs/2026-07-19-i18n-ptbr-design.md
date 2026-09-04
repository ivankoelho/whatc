# Design: Internacionalização completa em pt-BR

- **Data:** 2026-07-19
- **Branch:** `feature/pt-br-i18n`
- **Status:** Aprovado (design) — pendente plano de implementação

## Objetivo

Tornar o WhatC (fork do Whatomate) totalmente disponível em Português do Brasil,
deixando **apenas dois idiomas** na interface — English (`en`) e Português (Brasil)
(`pt-BR`) — com **pt-BR como idioma padrão**.

Traduzir somente a interface controlada pelo código (vue-i18n). **Não** traduzir
dados do banco, templates/mensagens de WhatsApp, nem conteúdo operacional salvo
pelos usuários.

## Contexto atual (estado do repositório)

- **Frontend:** Vue 3 + shadcn-vue. i18n via `vue-i18n` (Composition API,
  `legacy: false`) em `frontend/src/i18n/index.ts`.
- **Auto-descoberta de locales:** `import.meta.glob('./locales/*.json')` — adicionar
  ou remover um arquivo JSON adiciona/remove o idioma; o seletor se ajusta sozinho.
- **Seletor de idioma:** `frontend/src/components/LanguageSwitcher.vue`, usado em
  `layout/UserMenu.vue` e `views/settings/SettingsView.vue`. Itera sobre
  `SUPPORTED_LOCALES` (derivado dos arquivos presentes).
- **Locales existentes:** `en.json` (fonte, ~2.546 chaves), `es.json`, `hi.json`,
  `ar.json`, `ta.json`. **Não existe pt/pt-BR.**
- **Idioma padrão atual:** `getDefaultLocale()` → localStorage → idioma do
  navegador (`navigator.language.split('-')[0]`) → fallback `en`. `fallbackLocale: 'en'`.
- **Cobertura:** app já fortemente internacionalizado — ~1.521 chamadas `$t()` em
  66 arquivos. Views principais já usam i18n.
- **Textos hardcoded remanescentes (~200 pontos):** em ~38 componentes sem `$t`,
  concentrados nos editores de nós dos construtores de fluxo:
  `chatbot/ChatNodeProperties.vue` (~70), `calling/IVRNodeProperties.vue` (~42),
  `flow-builder/FlowBuilder.vue` (~20), `chatbot/PanelConfigEditor.vue` (~15),
  nós de chatbot/calling, `flow-preview/*` e alguns `shared/*`. Os primitivos
  `components/ui/*` (shadcn) não têm copy e ficam fora de escopo.
- **Gestão externa:** Crowdin (`crowdin.yml` na raiz e em `frontend/crowdin.yml`).

## Decisões de escopo (confirmadas com o usuário)

1. **Código do locale:** `pt-BR` (arquivo `pt-BR.json`, rótulo "Português (Brasil)").
2. **Textos hardcoded:** corrigir **todos** nesta mesma feature (cobertura 100%).
3. **Idiomas extras:** **apagar** `es.json`, `hi.json`, `ar.json`, `ta.json`.
4. **Backend Go:** **fora de escopo** — traduzir apenas a UI do frontend (vue-i18n).

## Design

Sem refatoração arquitetural. Mudanças cirúrgicas em quatro frentes.

### A. Locale pt-BR, idioma padrão e poda de idiomas — `frontend/src/i18n/index.ts`

1. Criar `locales/pt-BR.json`, espelho estrutural de `en.json` (mesmas chaves),
   com todos os valores traduzidos.
2. Adicionar em `localeNames`:
   `'pt-BR': { name: 'Portuguese (Brazil)', nativeName: 'Português (Brasil)' }`.
3. Tornar pt-BR o padrão:
   - Em `getDefaultLocale()`, quando não houver locale salvo válido, retornar `'pt-BR'`.
   - Ajustar a detecção de navegador para que `pt`, `pt-BR` e `pt-br` resolvam para
     `'pt-BR'` (a lógica atual `split('-')[0]` transforma `pt-BR` em `pt`, que não
     casaria com o arquivo `pt-BR.json`).
   - Mudar `fallbackLocale` de `'en'` para `'pt-BR'`.
4. Apagar `es.json`, `hi.json`, `ar.json`, `ta.json`. Com a auto-descoberta, o
   seletor passa a listar apenas **English** e **Português (Brasil)**.

### B. Extração dos textos hardcoded (~200 pontos, ~38 componentes)

Para cada componente sem `$t`:

- Adicionar as chaves novas em `en.json` (fonte) **e** em `pt-BR.json`, sob as
  seções existentes (`chatbot.*`, `calling.*`, `flows.*`, `common.*`, ...),
  seguindo a convenção `secao.subsecao.acao`.
- Substituir os literais por `$t('...')` / `t('...')`, cobrindo: texto visível,
  `placeholder`, `title`, `aria-label`, estados vazios (ex.: "No buttons"),
  toasts/notificações, tooltips e mensagens de validação.
- Ordem de ataque por volume: `ChatNodeProperties`, `IVRNodeProperties`,
  `FlowBuilder`, `PanelConfigEditor`, depois os nós (`chatbot/nodes/*`,
  `calling/nodes/*`) e `flow-preview/*`, por fim os `shared/*` restantes.
- Também varrer os 66 arquivos que já usam `$t` em busca de literais soltos
  remanescentes e convertê-los.

### C. Testes e configuração

- Reescrever `frontend/e2e/tests/settings/language-switch.spec.ts` (hoje 100%
  baseado em Espanhol) para pt-BR: dropdown "Español" → "Português (Brasil)";
  textos esperados "Configuración General"/"Panel"/"Configuración" →
  equivalentes pt-BR; `savedLocale` esperado `'es'` → `'pt-BR'`.
- Atualizar `crowdin.yml` / `frontend/crowdin.yml` (mapeamento do `pt-BR`) e o
  `frontend/src/i18n/README.md` (lista de idiomas suportados).

### D. Explicitamente fora de escopo

Dados do banco; templates e mensagens de WhatsApp; conteúdo operacional dos
usuários; mensagens do backend Go; primitivos `components/ui/*`.

## Riscos e mitigações

1. **Teste e2e quebra** (`language-switch.spec.ts` é todo em espanhol) →
   reescrever para pt-BR como parte da feature.
2. **Detecção de navegador** transforma `pt-BR` em `pt` → ajustar
   `getDefaultLocale()` para mapear ambos para `pt-BR`.
3. **Chaves faltando no pt-BR** caem no fallback silenciosamente (parecem "não
   traduzido") → script de paridade de chaves obrigatório na validação.
4. **Não tocar em dados** (templates/mensagens/DB) → só alteramos chaves de UI.
5. **Regressão de idioma padrão** para usuários com locale salvo — quem já tem
   `locale` no localStorage mantém sua escolha; o padrão pt-BR vale para novos.

## Estratégia de validação (cobertura total)

- **Script de paridade de chaves:** falha se `pt-BR.json` e `en.json` divergirem
  em qualquer chave (faltando ou sobrando).
- **Script anti-hardcode:** regex varrendo `.vue` fora de `ui/` por texto literal
  em `>...<` e em atributos `placeholder|title|label|aria-label`; a lista deve
  zerar (exceto exceções em allowlist, ex.: nomes próprios/ícones).
- **Build + e2e:** `npm run build` e o e2e de idioma reescrito passando.
- **Navegação manual** das telas-chave em pt-BR (login, dashboard, chat, chatbot,
  calling, settings) verificando ausência de texto em inglês.

## Fluxo de trabalho (skills Superpowers)

1. **brainstorming** → este documento de spec.
2. **writing-plans** → plano de implementação detalhado, faseado por lote de
   componentes.
3. **test-driven-development** (scripts de paridade/anti-hardcode + e2e como
   testes), **verification-before-completion**, **requesting-code-review** antes
   do merge, em worktree isolado (**using-git-worktrees**).
