# Painel do contato nos fluxos: dados cadastrais configuráveis e bloco vazio oculto

Nota de design. Estado: **aprovada para implementação**. Decisões: campo cadastral sem valor é omitido (rótulo incluído) e as variáveis de sessão seguem com `-`; seção sem conteúdo visível some; sem conteúdo visível, o bloco inteiro (nome do fluxo incluído) some; o cabeçalho de cadastro fica como está; chaves `contact:*`; sem mudança de schema, API ou backend; dados do contato resolvidos na renderização; regras do #89 respeitadas.

## 1. Objetivo
1. Em "Exibição do painel do contato" (editor do fluxo), permitir escolher como campos do painel: **Tipo, CPF/CNPJ, Unidade e Departamento** do contato, junto com as variáveis da sessão, dentro das seções existentes.
2. Não mostrar o bloco "Nenhum dado configurado..." quando não há configuração de painel nem sessão de fluxo.

## 2. Como funciona hoje (verificado)
- A configuração fica no fluxo (`chatbot_flows.panel_config`): `{sections:[{id,label,columns,collapsible,default_collapsed,order,fields:[{key,label,order,display_type,color}]}]}`. O campo `key` é o nome de uma variável da sessão (`store_as` de um prompt, ou chave de `response_mapping` de um api_call).
- `GET /api/contacts/{id}/session-data` devolve `panel_config` do fluxo da sessão mais recente e **só as chaves configuradas** de `session_data`. O backend não valida nem interpreta os `key`: uma chave que não existe na sessão simplesmente não vem.
- `ContactInfoPanel` mostra `session_data[key]` (ou `-` se vazio). Sem sessão ou sem seções, mostra o bloco "Nenhum dado configurado".
- O contato (`props.contact`) já chega ao painel pela store, que é atualizada quando o cadastro é salvo (PR #89/#90), e o cabeçalho do painel já mostra Tipo, CPF/CNPJ e, para colaborador, Unidade e Departamento.

## 3. Representação dos quatro campos (sem quebrar nada)
Chaves **reservadas**, em um espaço de nomes que nenhuma variável de sessão consegue ter (nomes de variável seguem `[a-zA-Z_][a-zA-Z0-9_]*` com pontos, nunca `:`):

| Chave | Campo | Origem |
|---|---|---|
| `contact:type` | Tipo | `contact.contact_type` |
| `contact:cpf_cnpj` | CPF/CNPJ | `contact.cpf_cnpj` |
| `contact:unit` | Unidade | `contact.unit_id` → nome |
| `contact:department` | Departamento | `contact.department_id` → nome |

- É o mesmo formato de campo (`key`, `label`, `order`, `display_type`, `color`): **nenhuma mudança de schema, de API ou de backend**. `display_type` badge/tag e `color` funcionam também para estes campos.
- **Dado atual, não cópia:** o valor vem do `contact` da store a cada renderização; nada é gravado na sessão. Editar o contato atualiza o painel na hora.
- **Por fluxo:** continua sendo `panel_config` do fluxo, escolhida no editor daquele fluxo.
- **Configurações antigas:** não têm chaves `contact:*`, então continuam exatamente como estão, sem migração. Variáveis da sessão não mudam. Uma versão antiga do frontend que receba um fluxo com chaves novas mostraria `-` nelas (só durante um rollout parcial).

## 4. Regras de exibição
- **Tipo:** rótulo traduzido (Cliente / Fornecedor / Colaborador); sempre tem valor.
- **CPF/CNPJ:** o valor como o cabeçalho mostra; **sem valor, o campo (e o rótulo) não aparece**.
- **Unidade / Departamento:** só para `colaborador` (regra do #89, `showsPlacement`). Para cliente e fornecedor o campo **não aparece**, mesmo configurado. Para colaborador, mostra o nome; se há o vínculo mas o usuário não tem acesso à lista (units:read/departments:read), mostra "Definido" (igual ao cabeçalho); sem vínculo, não aparece.
- **Campos sem valor:** os dados cadastrais sem valor são **omitidos** (não mostram rótulo vazio nem `-`). As **variáveis da sessão mantêm o comportamento atual** (`-`), para não alterar painéis existentes.
- **Seção sem nada visível** (todos os campos dela são cadastrais e estão omitidos): a seção inteira não aparece. Se nenhuma seção tem conteúdo visível, o bloco todo (inclusive o nome do fluxo) não aparece.

## 5. Bloco vazio
A condição passa a esconder o bloco inteiro quando não há sessão, ou o painel não tem seções, ou nenhuma seção tem conteúdo visível. As chaves `chat.noDataConfigured` e `chat.configurePanelHint` deixam de ser usadas pelo painel (a dica de configuração continua no editor do fluxo).

## 6. Mudanças (somente frontend)
- `lib/contact-registration.ts`: catálogo dos quatro campos, `isContactPanelField(key)` e a função pura que resolve um campo (`{visible, value}`) a partir do contato e dos nomes. Testada com vitest.
- Composable pequeno para carregar uma vez e compartilhar as listas de unidades e departamentos (hoje `ContactRegistrationSection` carrega as suas): evita duas buscas.
- `PanelConfigEditor.vue`: o seletor "Adicionar campo" ganha o grupo "Dados do contato" (os quatro, sem repetir os já usados), com rótulos amigáveis e o nome do campo no lugar da chave; as variáveis da sessão ficam como estão.
- `ContactInfoPanel.vue`: usa a função de resolução nos campos de painel; filtra campos e seções vazios; esconde o bloco vazio.
- i18n pt-BR e en. Backend, API, schema e `panel_config` armazenado: **sem alteração**.

## 7. Testes
- vitest: resolução de cada campo (cliente, fornecedor, colaborador; com e sem valor; sem acesso à lista); chaves reservadas não colidem; seção sem conteúdo some.
- Verificação no navegador: configurar os quatro campos em um fluxo de teste (`E2E-`), conferir a ordem, a mudança ao editar o contato, cliente sem unidade/departamento, CPF vazio omitido, painel antigo igual, e o bloco vazio escondido. Playwright `contact-info.spec.ts` rodado de novo.

## 8. Fora de escopo
Nova tela ou módulo; variáveis da sessão; backend; edição do cadastro pelo painel de fluxo (continua no lápis do cabeçalho); reordenar ou remover o cabeçalho de cadastro.

## 9. Plano
Branch nova a partir de `development` (`888eb57`), commit da nota primeiro, depois a implementação em commits pequenos, testes, PR, revisão. Sem merge sem autorização explícita.
