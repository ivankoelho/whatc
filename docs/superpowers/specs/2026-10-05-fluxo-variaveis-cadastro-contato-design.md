# Variáveis automáticas do cadastro do contato nos fluxos

Nota de design. Estado: **implementada** na branch `feature/flow-contact-placement-variables` (sem push nem PR até autorização).

## Problema
Unidade e departamento do contato só entravam no fluxo por roteamento (nó Transferir) e por exibição (painel do contato). Não havia como escrever o **nome** da unidade numa mensagem.

## Decisão
O runner de grafo (`runChatGraph`) semeia, junto de `phone_number` e `contact_name`, três variáveis de sessão a partir do contato no banco:

| Variável | Valor |
|---|---|
| `{{contact_type}}` | `cliente`, `fornecedor` ou `colaborador` (vazio vira `cliente`) |
| `{{contact_unit}}` | nome da unidade; `""` se não for colaborador ou sem unidade |
| `{{contact_department}}` | nome do departamento; `""` se não for colaborador ou sem departamento |

- Sempre definidas, para que um valor ausente vire texto vazio e nunca o `{{...}}` literal.
- Nomes buscados dentro da organização do contato (`organization_id`); duas consultas por execução do grafo. Unidade/departamento inativos continuam mostrando o nome do vínculo existente.
- Unidade e departamento só existem para colaborador (regra do servidor, PR #89); para os demais tipos os dois valem `""`.
- **CPF/CNPJ não vira variável** (dado sensível, iria para mensagens, URLs e corpos de API). Continua só no painel (`contact:cpf_cnpj`).
- Sem mudança de schema, API ou rotas. Os nomes `contact_type`, `contact_unit` e `contact_department` passam a ser reservados: um `store_as` igual seria sobrescrito a cada execução.

## Mudanças
- `chatbot_graph_runner.go`: `seedContactRegistrationVars`.
- Simulação do editor (`useFlowGraphSimulation.ts`): valores de mentira para as três.
- Docs: tabela "Built-in variables" de `chatbot.mdx`.
- Teste: `TestFlowVariables_ContactRegistration` (colaborador completo, só unidade, cliente).
