# Homologação do antimalware em Linux (isolada)

Reproduz, em uma rede Docker própria, o que importa da arquitetura da Contabo para a política de antimalware: host Linux, PostgreSQL 17, Redis 7 e um contêiner ClamAV (clamd + freshclam) **sem nenhuma porta publicada**. Nada aqui usa credenciais ou dados de produção; todos os segredos são descartáveis e valem só dentro desta rede.

```bash
scripts/homologation/antimalware.sh          # roda as fases A–D e mantém o ambiente
scripts/homologation/antimalware.sh --down   # idem, e remove contêineres e volumes ao final
```

Requisitos: Docker (Linux ou Docker Desktop com contêineres Linux) com acesso à internet na primeira execução (imagens, assinaturas do ClamAV, módulos Go). A primeira execução baixa as assinaturas (alguns minutos); as seguintes reaproveitam o volume `clamav-db`.

## Como funciona

Cada fase é uma execução de `go test` (`internal/handlers/homologation_test.go`) no contêiner `runner`, sobre o **mesmo** banco e o **mesmo** diretório de armazenamento (volume Docker `storage`, dentro da VM Linux). O script para, reinicia e atualiza o motor entre as fases. As fases só rodam com `TEST_CLAMD_ADDR` definido, então a suíte normal nunca precisa de um motor.

| Fase | Motor | Cenários |
|---|---|---|
| A | ativo | 1 arquivo limpo · 2 EICAR · 3 quarentena (arquivo sai do caminho servido, assinatura registrada, auditoria) · 7 arquivos antigos sem veredito varridos em lotes · 9 download bloqueado (inclusive `Range`) · 11 organização/usuário sem acesso · 12 três workers concorrentes na mesma fila |
| B | parado, e "travado" (`tarpit`: aceita e nunca responde) | 4 scanner indisponível · 6 timeout: nada é liberado, antigos continuam fechados |
| C | reiniciado | 5 scanner reiniciado · 10 recuperação: o que falhou é reprocessado e liberado; o infectado continua infectado |
| D | `freshclam` + `clamdscan --reload` | 8 atualização de assinaturas: arquivos limpos varridos com assinaturas mais antigas são rechecados |

Evidências (logs de cada fase, versões do motor antes/depois, resultado) ficam em `docker/homologation/evidence/<data-hora>/` (ignorado pelo git).

## O que NÃO é reproduzido aqui

- **Desempenho do servidor real** (CPU, RAM, disco, latência de rede da Contabo): os limites do compose (`2 cpus / 3 GB` para o ClamAV) são estimativas. Rode `whatomate antimalware report` em um **clone** do banco de produção para o volume e o tempo da varredura inicial.
- **Atualização real de assinaturas**: `freshclam` roda, mas se não houver publicação nova entre as duas medições a versão não muda. A *revarredura* é validada forçando versões antigas nas linhas; o download de assinaturas em si depende do espelho do ClamAV.
- **Dois hosts** (aplicação e `clamd` em máquinas diferentes) e a restrição de rede (firewall/porta 3310 só para o backend): documentados, mas dependem da rede real.
- A interface (selos, atualização automática) é validada no navegador e nos testes do frontend, não neste ambiente.

## Limpeza

```bash
docker compose -p whatc-homolog -f docker/homologation/docker-compose.yml --profile run down -v
```
