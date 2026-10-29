# Design — dbui, cliente PostgreSQL em TUI (Go)

## Contexto

Queremos um cliente de banco de dados no terminal, escrito em Go, para gerenciar várias instâncias PostgreSQL.
Funcionalidades do MVP:
1. Cadastrar uma instância e conectar nela.
2. Navegar pela hierarquia instância → databases → schemas → tabelas.
3. Escrever e executar SQL.
4. Ver o resultado: uma grade de registros quando a query devolve linhas, ou o command tag (ex.: `UPDATE 3`) quando não devolve.

Decisões já tomadas com o usuário:
- **Engine:** só PostgreSQL. Interface de driver mínima para permitir outras engines depois.
- **Conexões:** `connections.yaml` em `os.UserConfigDir()/dbui/` e a senha no keychain do SO.
- **Layout:** uma aba por conexão aberta. Cada aba tem 3 painéis: Explorer à esquerda, Query em cima à direita e Results embaixo.
- **Segurança:** qualquer SQL é permitido. Cada conexão tem as flags `read_only` e `production`, e `production` pede confirmação antes de escrita.

Nome provisório do projeto: `dbui`, num repositório novo.

## Abordagens consideradas

| Opção | Resumo | Veredito |
|---|---|---|
| **A. Bubble Tea v2 + Lip Gloss v2 + Bubbles v2 + Huh** | Elm Architecture. Todo I/O em `tea.Cmd`, componentes próprios para a árvore e a grade | **Recomendada.** Muito estado concorrente (várias abas, queries em andamento, carga sob demanda) cabe bem no MVU, e é fácil de testar |
| B. tview | Table, TreeView e Form prontos | Entrega rápida, mas o estado mutável com callbacks fica frágil com abas e queries assíncronas. Teste mais difícil |
| C. tcell puro | Engine própria | Custo alto demais para o MVP |

## Stack

- Go 1.25+.
- `charm.land/bubbletea/v2`, `lipgloss/v2` e `bubbles/v2` (textarea, viewport, help/key, spinner, textinput).
- `charmbracelet/huh` para o formulário de conexão.
- `github.com/jackc/pgx/v5` com `pgxpool`: tipos nativos, cancelamento por `context` e `FieldDescriptions`.
- `github.com/zalando/go-keyring` para as senhas e `gopkg.in/yaml.v3` para a configuração.
- `spf13/cobra`, só se houver subcomandos. No MVP basta `dbui [nome-da-conexão]`.
- Testes: `testcontainers-go/modules/postgres` e `x/exp/teatest`.

## Arquitetura (pacotes)

```
cmd/dbui/main.go
internal/config/      # carrega e salva connections.yaml (struct Connection, sem senha)
internal/secret/      # wrapper do go-keyring: Get/Set/Delete(connID)
internal/db/          # interface + implementação Postgres
  db.go               # type Session interface { ListDatabases, ListSchemas, ListTables, Exec, Close }
  postgres/           # pgxpool por (conexão, database); queries de catálogo; executor
internal/tui/
  app.go              # modelo raiz: barra de abas, lista de sessões, roteamento de mensagens, modais
  connlist/           # tela inicial / modal: lista de conexões salvas (abrir, novo, editar, apagar)
  connform/           # formulário Huh (host, porta, user, senha, db padrão, sslmode, flags)
  session/            # modelo de uma aba: dono do Explorer, Editor e Results, além do foco
  explorer/           # árvore própria com carga sob demanda (nenhum componente pronto atende)
  editor/             # bubbles/textarea + atalho para abrir no $EDITOR via tea.ExecProcess
  results/            # grade própria com scroll horizontal/vertical, ou mensagem/erro
  confirm/            # modal de confirmação (escrita em produção)
  styles.go, keys.go  # tema Lip Gloss e keymap central (bubbles/key + help)
```

Cada unidade tem uma responsabilidade só. `internal/db` não conhece a TUI, e a TUI só fala com `db.Session`.

## Fluxo de dados

- **Async padrão:** toda chamada ao banco é um `tea.Cmd` que devolve uma mensagem com `sessionID` e `reqID`. Resposta com `reqID` antigo é descartada, o que evita corrida quando o usuário navega rápido.
- **Database = pool:** no Postgres a conexão é amarrada a um database. A `Session` guarda um mapa `database → *pgxpool.Pool`, criado de forma lazy quando o usuário expande um database ou troca o database ativo do editor (mostrado no título do painel Query).
- **Catálogo:**
  - `pg_database` filtrando `datistemplate = false`;
  - `pg_namespace`, excluindo `pg_toast` e `pg_temp_*`, com opção de mostrar os schemas de sistema;
  - `pg_class` com `relkind IN ('r','p','v','m','f')`, agrupado em Tables e Views.
- **Execução:**
  - `Ctrl+E` executa o texto selecionado ou o buffer inteiro.
  - Se `rows.FieldDescriptions()` não estiver vazio, é resultado tabular. Isso cobre `SELECT`, `WITH`, `RETURNING`, `SHOW` e `EXPLAIN`, então não dependemos de fazer parse de "SELECT".
  - Senão, mostra o `CommandTag`.
  - Lê no máximo **1000 linhas** (configurável). Se houver mais, mostra "mostrando 1000+ linhas, truncado".
  - Duração e quantidade de linhas vão na borda do painel Results.
- **Enter em uma tabela no Explorer** gera `SELECT * FROM "schema"."tabela" LIMIT 200`, coloca no editor e executa.
- **Cancelar:** `Ctrl+C` com query em andamento cancela o `context`, e o pgx envia o cancel request ao servidor. Sem query em andamento, `Ctrl+C` / `q` sai do app, pedindo confirmação se houver abas abertas.
- **Valores:** NULL aparece como `∅` esmaecido; bytea em hex truncado; json compacto. Células longas são cortadas com `…`. `Enter` numa célula abre o valor completo num modal com viewport.

## Segurança das conexões

- `read_only: true` → `AfterConnect` do pool roda `SET default_transaction_read_only = on`.
- `production: true`:
  1. A query roda primeiro dentro de `BEGIN READ ONLY`.
  2. Se falhar com SQLSTATE `25006` (read_only_sql_transaction), é rollback e aparece o modal "Esta conexão é de PRODUÇÃO. Executar comando de escrita?".
  3. Se o usuário confirmar, a query roda de novo normalmente.

  Isso evita fazer parse de SQL. Contrapartida: a parte de leitura de uma query de escrita roda duas vezes.
- A aba de uma conexão `production` tem uma cor de destaque vermelha.

## Tratamento de erros

- Erro do Postgres (`*pgconn.PgError`) aparece no painel Results com `Severity`, `Code`, `Message`, `Detail` e `Hint`. Se houver `Position`, o cursor do editor vai para essa posição.
- Falha de conexão (rede, auth): erro no lugar da árvore do Explorer e ação `r` para reconectar. As outras abas não são afetadas.
- Keychain indisponível (ex.: Linux sem secret service): cai para pedir a senha a cada conexão, sem salvar.

## Testes

- `internal/db/postgres`: testes de integração com testcontainers. Cobrem catálogo, resultado tabular vs. command tag, truncamento, cancelamento, read-only e o fluxo do `25006`.
  - No Colima, exportar antes `DOCKER_HOST="unix://$HOME/.colima/default/docker.sock"` e `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock`.
- `internal/config` e `internal/secret`: unitários (keyring com `keyring.MockInit()`).
- `internal/tui/*`: testes de `Update` (mensagem → estado/comando) com um `db.Session` fake, e golden de `View()` com tamanho fixo.
- `teatest`: fluxo ponta a ponta (abrir conexão fake → expandir árvore → executar → ver a grade).

## Riscos e pontos a validar

- **Grade de resultados:** o `bubbles/table` não tem scroll horizontal. `evertras/bubble-table` tem, mas é preciso confirmar se suporta Bubble Tea v2. Plano: um componente próprio e simples (larguras calculadas por amostra das linhas, janela de colunas e linhas visíveis).
- **Destaque de sintaxe no editor:** fora do MVP. O `textarea` não suporta, e o `$EDITOR` externo resolve no curto prazo.

## Fora do MVP (YAGNI)

Outras engines, histórico de queries, autocomplete, edição de células na grade, exportar CSV, túnel SSH, DDL viewer.
São candidatos naturais para a v2.

## Próximos passos (após aprovação)

1. Criar o repositório `dbui`, salvar este design em `docs/superpowers/specs/2026-10-08-dbui-design.md` e fazer commit.
2. Gerar o plano de implementação detalhado (skill writing-plans), em fatias verticais:
   1. config + keychain + formulário de conexão;
   2. sessão + Explorer;
   3. editor + execução + grade;
   4. abas;
   5. segurança de produção.

## Verificação

- `go test ./...`: unitários e TUI, com o env do Colima para a integração.
- Manual: `docker run postgres:17` local, cadastrar duas conexões (uma marcada como `production`), abrir as duas em abas, navegar até uma tabela, executar `SELECT`, `UPDATE`, uma query inválida (deve posicionar o cursor no erro) e uma query lenta (`SELECT pg_sleep(30)`) cancelada com `Ctrl+C`.

## Decisões tomadas na implementação (2026-10-13)

- **Conexão dedicada do editor por database** (além de um pool pequeno para o catálogo): `SET` e transações
  `BEGIN … COMMIT` em execuções separadas funcionam como no `psql`.
- **Execução via protocolo simples** (`PgConn.Exec`): aceita vários statements, valores chegam em texto (igual ao
  `psql`); o resultado exibido é o último que retornou linhas, senão o último command tag. Linhas além de 1000 são
  contadas mas não guardadas.
- **Guard de produção**: fora de transação usa `BEGIN READ ONLY … COMMIT`; dentro da transação do usuário usa
  `SAVEPOINT` + `SET LOCAL transaction_read_only = on` + `ROLLBACK TO SAVEPOINT`. Comandos de controle de transação
  rodam direto; misturados com outros statements pedem confirmação (não há como sondar com segurança).
- **Cancelamento** usa `CancelRequestContextWatcherHandler` do pgconn (cancel request ao servidor em vez de fechar a
  conexão).
- **Enter numa tabela** anexa o `SELECT … LIMIT 200;` ao buffer (não sobrescreve o que o usuário escreveu).
- **Execução**: seleção do textarea, senão o statement sob o cursor (`internal/sqlsplit`).
- **Teclas**: `ctrl+e`/`f5` executa (o `End` do textarea fica só em `end`), `ctrl+x` fecha aba, `alt+1…9` e
  `alt+[`/`alt+]` trocam aba, `ctrl+c` cancela query em execução e, ociosa, pede confirmação para sair (2º `ctrl+c`
  sai direto).
- **Senha**: sem senha no keychain conecta sem senha (respeita `~/.pgpass`); falha de autenticação abre prompt e salva
  a senha informada. Keychain indisponível também abre o prompt.
- **Grade de resultados**: componente próprio (o `bubbles/table` não tem scroll horizontal).
- Schemas de sistema (`pg_*`, `information_schema`) ficam ocultos no MVP.
