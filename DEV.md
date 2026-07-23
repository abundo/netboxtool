# Netboxtool development

## Layout

- `netboxtool.go` — `NetboxClient`, HTTP plumbing (GraphQL POST + REST
  PATCH/POST/DELETE), pagination loop, and the `Get*`/`Update*`/`*Create`/
  `*Delete` API methods.
- `netboxtool_graphql.go` — the raw GraphQL query bodies
  (`deviceListGraphQLbody`, `virtualMachineListGraphQLbody`,
  `deviceTypeGraphQLbody`, `manufacturerListGraphQLbody`).
- `models.go` — `NBDevice`/`NBInterface`/`NBAddress`/`NBTag`, the shapes
  returned to callers (also carry `gorm` tags for consumers that persist
  them).
- `cmd/netboxtool_cli.go` — the CLI, one `boa.CmdT` subcommand per API method.

## GraphQL query architecture

Netbox exposes both a GraphQL endpoint (read-only) and a REST API
(read/write). Reads (`Get*`) go through GraphQL; writes (`Update*`,
`*Create`, `*Delete`) go through REST, since Netbox's GraphQL API doesn't
support mutations.

`deviceListGraphQLbody`/`virtualMachineListGraphQLbody` fetch a device/VM
together with all its related objects — `device_type` (+ its
`manufacturer`), `role`, `site`, `platform`, `tags`, and `interfaces` (+
each interface's `tags` and `ip_addresses`) — **nested inline in one
query**, rather than fetching each table flat and stitching the results
together in Go (a `map[uint]T` keyed by foreign id, joined client-side).

This is a deliberate, previously-revisited decision:

- An earlier version fetched flat tables (`device_type_list`, `site_list`,
  `role_list`, `platform_list` fetched separately, joined via lookup maps in
  Go) because a benchmark suggested it was faster, even for to-one
  relations.
- That flat/stitch approach was later reverted back to nested queries at
  the user's explicit request. **Don't reintroduce flat fetch-and-stitch
  without asking first** — it was deliberately abandoned once already, so a
  benchmark alone isn't sufficient justification to bring it back.

Filtering a single device/VM by name or id happens via
`filters: { name/id: { exact: ... } }` on `device_list`/
`virtual_machine_list` itself (see `NetboxAPICall`), which also scopes the
nested `interfaces`/`ip_addresses` for free since they're part of the same
query.

## Pagination

Netbox's GraphQL API caps a single query's result at `graphqlPageSize`
(1000 rows) regardless of the requested `limit`. `fetchAllPages`/
`fetchAllPagesRaw` (`netboxtool.go`) loop until a page comes back short.

Pagination is **cursor-based**, not offset-based:
`pagination: { start: <id>, limit: N }`, where `start` is the Netbox object
id to resume from (`id >= start`), not a row offset. Netbox returns rows
ordered by id ascending, so each loop iteration sets `start` to
`lastID + 1`. This matters because offset-based pagination
(`pagination: { offset: <n>, limit: N }`) gets more expensive per page as
`offset` grows — Postgres has to scan and discard `offset` rows — while
cursor pagination is a constant-cost index seek regardless of position.

## Performance notes / things already tried

Netbox's REST/GraphQL API was originally much slower for a full fetch than
an equivalent script run in Netbox's Django `nbshell` (in-process ORM, no
HTTP). Root causes found and fixed:

1. **HTTP connection reuse** — `NetboxClient` holds one shared
   `http.Client` (built once in `NewNetboxClient`), instead of a fresh
   `http.Transport`/`http.Client` per call. Any new HTTP call added to the
   client should reuse `nb.httpClient`, not construct its own.
2. **Server-side uwsgi worker count** — a single-worker (`processes = 1`)
   uwsgi config on the Netbox host bottlenecked every client regardless of
   how the Go side was optimized. If fetch performance regresses, check the
   Netbox host's uwsgi `processes`/`threads` config before assuming it's a
   client-side problem.
3. **Cursor vs offset pagination** — see above.

**Tried and reverted, do not reintroduce without asking:**

- Goroutine/`sync.WaitGroup`-based concurrent fetching of lookup tables.
  Measured to give no benefit once (2) above was fixed — the uwsgi fix was
  the actual source of the earlier speedup, not client-side concurrency.
- Flat fetch-and-stitch GraphQL queries (see "GraphQL query architecture"
  above) — reverted back to nested queries at the user's request.

## Testing

There are currently no automated tests (no `*_test.go` files). Verifying
changes means running the CLI against a real (or test) Netbox instance with
a valid config file (see [README.md](README.md#configuration)).
