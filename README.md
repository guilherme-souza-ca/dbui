# dbui

A terminal client for PostgreSQL, built with [Bubble Tea v2](https://charm.land).

- Save several instances; passwords go to the OS keychain.
- One tab per connection, each with an **Explorer** (databases → schemas → tables/views),
  a **Query** editor and a **Results** grid.
- Run the statement under the cursor (or the selection) with `ctrl+e`; cancel with `ctrl+c`.
- Tabular results (`SELECT`, `RETURNING`, `SHOW`, `EXPLAIN`…) open in a scrollable grid;
  other statements show their command tag (`UPDATE 3`).
- Per-connection safety flags: **read-only** (every transaction is read-only) and
  **production** (writes ask for confirmation, tab shown in red).

## Install / run

```bash
go install github.com/guilhermesouza/dbui/cmd/dbui@latest   # or: go build ./cmd/dbui
dbui                 # opens the connection list
dbui local-pg        # connects to a saved connection right away
dbui -log /tmp/dbui.log   # debug log (stdout is used by the UI)
```

Connections are stored in `$(os.UserConfigDir)/dbui/connections.yaml`
(`~/Library/Application Support/dbui/` on macOS, `~/.config/dbui/` on Linux), mode 0600,
without passwords. When no password is stored dbui connects without one, so `~/.pgpass`
and `trust` auth keep working; on an authentication failure it prompts for the password
and saves it in the keychain.

## Keys

| Where | Keys |
|---|---|
| Global | `ctrl+o` connections · `ctrl+x` close tab · `alt+1…9` / `alt+[` `alt+]` switch tab · `f1` help · `ctrl+c` quit |
| Tab | `tab` / `shift+tab` move between panes · `ctrl+e` / `f5` run · `ctrl+c` cancel running query · `f2` edit in `$EDITOR` |
| Explorer | `↑↓` move · `enter` expand + use database / open table · `→` expand · `←` collapse · `r` refresh |
| Editor | normal editing · `shift+arrows` select · `ctrl+g` select all |
| Results | `↑↓←→` / `hjkl` move · `pgup/pgdn` · `g`/`G` first/last row · `0`/`$` first/last column · `enter` view full cell (JSON is indented) · `y` copy cell |

Opening a table appends `SELECT * FROM "schema"."table" LIMIT 200;` to the editor and runs it.
Results keep at most 1000 rows in memory; the title shows `showing 1000 of N rows` when truncated.

## How the production guard works

For connections marked *production* (and not read-only), each execution first runs inside
`BEGIN READ ONLY` (or a savepoint with `transaction_read_only = on` when you are inside your
own transaction). Reads behave normally. If the statement tries to write, PostgreSQL fails
with `25006`, the probe is rolled back and dbui asks for confirmation before running it for
real. Transaction control (`BEGIN`, `COMMIT`, …) runs directly; mixing it with other
statements in one execution always asks.

Each database has a dedicated editor connection, so `SET` and multi-step transactions
(`BEGIN` … `COMMIT` across executions) behave like `psql`.

## Development

```bash
go test -short ./...    # unit + TUI tests (no Docker)
go test ./...           # + PostgreSQL integration tests (testcontainers)
```

On Colima, export these before the integration tests:

```bash
export DOCKER_HOST="unix://$HOME/.colima/default/docker.sock"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
```

Layout:

```
cmd/dbui/              entrypoint
internal/config        connections file
internal/secret        OS keychain
internal/sqlsplit      statement splitting (quotes, comments, dollar quotes)
internal/db            Session interface; postgres/ implements it with pgx
internal/tui           root model (tabs, overlays, status bar)
  session/             one tab: explorer + editor + results, query execution
  explorer/ editor/ results/ connlist/ connform/ confirm/ styles/
  tuitest/             fake session, key helpers, synchronous cmd runner
```
