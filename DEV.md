# Netboxtool development

## Layout

- `netboxtool.go` — `NetboxClient`, HTTP plumbing (GraphQL POST + REST
  PATCH/POST/DELETE), pagination loop, and the `Get*`/`Update*`/`*Create`/
  `*Delete` API methods.
- `netboxtool_graphql.go` — the raw GraphQL query bodies, both the nested
  ones (`deviceListGraphQLbody`, `virtualMachineListGraphQLbody`,
  `deviceTypeGraphQLbody`, `manufacturerListGraphQLbody`, ...) and the flat
  ones `GetDevices`/`GetVMs` use instead (`flatDeviceListGraphQLbody`,
  `interfaceListGraphQLbody`, `flatVirtualMachineListGraphQLbody`,
  `vmInterfaceListGraphQLbody`, `ipAddressListGraphQLbody`) — see
  "GraphQL query architecture" below for why there are two shapes.
- `netboxtool_flat.go` — the flat-query fetch functions and the
  `stitchDevices`/`stitchVMs` functions that reassemble flat
  devices/interfaces/addresses back into the nested `[]JSONDevice` shape
  `parseDevices` expects, plus `getDevicesFlat`/`getVMsFlat` (what
  `GetDevices`/`GetVMs` actually call).
- `models.go` — `NBDevice`/`NBInterface`/`NBAddress`/`NBTag`, the shapes
  returned to callers (also carry `gorm` tags for consumers that persist
  them).
- `cmd/netboxtool_cli.go` — the CLI, one `boa.CmdT` subcommand per API
  method, plus two diagnostic-only subcommands: `benchmark` (compares fetch
  strategies with timing, see "Performance notes" below) and
  `introspect-ip-address` (read-only GraphQL schema introspection, used to
  find `assigned_object`'s real union member type names rather than
  guessing them).

## GraphQL query architecture

Netbox exposes both a GraphQL endpoint (read-only) and a REST API
(read/write). Reads (`Get*`) go through GraphQL; writes (`Update*`,
`*Create`, `*Delete`) go through REST, since Netbox's GraphQL API doesn't
support mutations.

There are two different query shapes in use, for two different call
patterns:

- **`GetDevice(name, id)`/`GetVM(name, id)`** (via `GetDevices_`/`GetVM_`)
  fetch **one** device/VM by name or id, filtered server-side
  (`filters: { name/id: { exact: ... } }` on `device_list`/
  `virtual_machine_list`, see `NetboxAPICall`). These still use the nested
  query bodies (`deviceListGraphQLbody`/`virtualMachineListGraphQLbody`),
  which resolve `device_type`/`role`/`site`/`platform`/`tags` and
  `interfaces` (+ each interface's `tags`/`ip_addresses`) all **nested
  inline in one query**. Nesting is fine, even preferable, for one row —
  the cost problem below only shows up at table scale.
- **`GetDevices()`/`GetVMs()`** (the full-table fetch, no name/id filter)
  instead use `getDevicesFlat`/`getVMsFlat` (`netboxtool_flat.go`): three
  separate flat top-level queries — `device_list`/`virtual_machine_list`
  (no nested `interfaces`), `interface_list`/`vm_interface_list` (no
  nested `ip_addresses`, plus a `device`/`virtual_machine` back-reference
  the nested version got for free from nesting), and a single shared
  `ip_address_list` (resolving its owning interface via `assigned_object`,
  a polymorphic union - see "Flattening GetDevices/GetVMs" below) — run
  **concurrently**, then stitched back into the exact same
  `[]JSONDevice{Interfaces: []JSONInterface{IPAddresses: [...]}}` shape the
  nested query used to produce directly. `parseDevices` (the actual
  device/interface/address field-mapping logic — status, lat/long
  inheritance, custom fields, ...) is unchanged either way; only how the
  raw data is fetched differs.

### A previous, narrower flattening attempt — and why this one is different

An earlier version of this package flattened `device_type_list`/
`site_list`/`role_list`/`platform_list` (small to-one lookup tables) into
separate queries joined via Go-side lookup maps, because a benchmark
suggested it was faster even for those to-one relations. That was reverted
back to nested queries at the user's request once a server-side
uwsgi-worker-count fix (see "Performance notes" below) made the nested
version fast enough on its own — the added stitching complexity wasn't
worth it for cheap single-row FK resolutions.

**That finding is still valid and still applies to those four lookup
tables — don't re-flatten `device_type`/`role`/`site`/`platform` based on
this section.** The flattening described above (`GetDevices`/`GetVMs`) is a
different, independently-benchmarked case: it targets the **nested
`interfaces{ip_addresses{...}}` one-to-many collection**, not a to-one
lookup, and — per the measurements below — has a severe, worse-than-linear
cost as interface count grows, an effect the earlier small-table
experiment would never have surfaced. Don't use this section to justify
flattening anything else without measuring it the same way first (see
`cmd/benchmark.go`).

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

Netbox's standard REST DRF pagination (`?limit=`/`next`, used by
`GetCables` and by the now-abandoned flat-REST variant benchmarked below)
is a different mechanism entirely, not covered by the cursor-vs-offset
comparison above — see "Flattening GetDevices/GetVMs" for what was found
benchmarking REST specifically for interfaces/addresses. `GetCables` itself
wasn't part of that benchmark and hasn't been measured the same way.

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

- Flat fetch-and-stitch for the small to-one lookup tables
  (`device_type_list`/`site_list`/`role_list`/`platform_list`) — no
  measured benefit once (2) above was fixed. This is a *different* scope
  than "Flattening GetDevices/GetVMs" below — don't cite that section as
  justification for re-flattening these small tables too.
- Goroutine/`sync.WaitGroup`-based concurrent fetching of those same small
  lookup tables — likewise no measured benefit once (2) was fixed. (Also a
  different scope than the concurrent fetching `getDevicesFlat`/
  `getVMsFlat` do now — see below.)

### Flattening GetDevices/GetVMs (2026-08)

`GetDevices()`/`GetVMs()` (full-table, no filter) were measured as the
actual bottleneck in a real factum2 full sync — not because of N+1 *REST*
calls (there weren't any: the old nested query fetched everything in one
page-cursored GraphQL query per page), but because Netbox's GraphQL
resolver does real per-row work server-side that a flatter query shape
avoids. At the time this was written that looked like an inherent cost of
resolving a nested one-to-many field; several concrete, since-fixed Netbox
bugs turned up afterward that better explain it — see "Upstream Netbox
bugs" below before assuming nested-vs-flat is the whole story. Investigation
and benchmark tooling: `cmd/benchmark.go`
(seven fetch-strategy variants, labeled A–G, with per-phase timing and
page counts) and `cmd/introspect.go`'s `introspect-ip-address` subcommand
(read-only schema introspection, used once to find `assigned_object`'s real
union member types instead of guessing them — see below).

**Measurements**, two real Netbox instances:

- Small: 2 CPUs, 6 uwsgi workers, 279 devices / 7,965 interfaces / 1,302
  addresses. Old nested query: **10.15s** (fetch only). Flat queries,
  interfaces+addresses fetched concurrently: **2.3–2.8s**.
- Large: Netbox 4.5.9 in Docker, 2 CPUs, 6 granian workers, 1,375 devices /
  37,021 interfaces / 3,794 addresses. Old nested query: **2m47.7s**
  (fetch only). Flat queries, concurrent: **20.2s** — **~8x faster**.
  A real end-to-end `factum-netbox sync` (fetch + DB writes + cable/site/
  tenant sync) against the small instance afterwards: 33.7s wall time,
  correct output (`0 new, 325 updated` — 279 devices + ~46 VMs, matching
  what a re-sync of already-known devices should show).

**What didn't work, and why it's not just "REST vs GraphQL":**

- A fully flat *REST* variant (`/api/dcim/devices/`, `/api/dcim/
  interfaces/`, `/api/ipam/ip-addresses/`, paginated) was **worse than the
  original nested GraphQL query** — 34–37s on the small instance, over 3
  minutes on the large one. The REST interfaces endpoint specifically did
  the same number of page round trips as the equivalent flat GraphQL query
  (verified via page counts) but took roughly 10x longer per page, meaning
  it's not a pagination/round-trip problem — something in Netbox's REST
  interface serializer itself is expensive per row (a likely Django-side
  N+1, e.g. a missing `prefetch_related`), independent of payload size or
  request count.
- On the large instance, the REST interfaces call appeared to leave the
  server measurably degraded for the *next* request too: a GraphQL
  `interface_list` fetch that normally took ~9s took over a minute
  immediately after the REST interfaces call, then returned to ~9s once
  more time had passed. Avoid REST bulk-list endpoints in any
  latency-sensitive path on a shared/production Netbox instance — the cost
  isn't necessarily contained to the one slow request.
- `ip_address_list`'s `assigned_object` field is a polymorphic union
  (`IPAddressAssignmentType` on Netbox 4.5.9, with `InterfaceType`/
  `VMInterfaceType`/`FHRPGroupType` as concrete members) — resolved via
  inline fragments (`... on InterfaceType { id }`). The member type names
  were found via `introspect-ip-address`, not guessed — a first guess at
  the VM interfaces query field name (`vminterface_list` instead of the
  real `vm_interface_list`) shipped to production once and crashed on
  first real use; verify against the actual schema before trusting a
  Netbox GraphQL field/type name pulled from memory or convention.

**Known accepted tradeoff**: `getVMsFlat` re-fetches `ip_address_list`
independently of `getDevicesFlat`, so a full sync (which calls
`GetDevices()` then `GetVMs()`) pays for that fetch twice. Given VMs are
typically a small fraction of these inventories, this was the simpler
first cut over adding cross-call caching to `NetboxClient` — which
`cache.go`'s own doc comment says deliberately never caches anything
itself. Revisit only if this shows up as real cost in production timing.

### Upstream Netbox bugs behind much of this (fixed in v4.6.7/v4.6.8)

Both instances above were benchmarked against Netbox 4.5.9. After
benchmarking, four independent, previously-unknown-to-us N+1/caching bugs
in Netbox's GraphQL layer turned up, all found and fixed within the same
short window, all released by **v4.6.8** — meaning the "why was the nested
query so slow" answer is broader than "resolving a nested one-to-many
field is inherently expensive" (this doc's working theory at the time):

- [**#22787**](https://github.com/netbox-community/netbox/issues/22787) —
  `IPAddressType.assigned_object` (the exact field `ipAddressListGraphQLbody`
  selects) issued ~8 SQL queries per row instead of a handful total. Fixed
  in v4.6.8. The underlying cause (a `GenericForeignKey` field with no
  `only=[...]`/`GenericPrefetch` optimizer hint) is called out by a
  maintainer as likely affecting *every* GFK-backed GraphQL field in
  Netbox, not just this one - watch for similar fixes to
  `L2VPNTerminationType.assigned_object`, `MACAddressType.assigned_object`,
  etc. in later releases.
- [**#22813**](https://github.com/netbox-community/netbox/issues/22813) —
  requesting `custom_fields` on *any* GraphQL list endpoint cost one extra
  single-row query per object returned (a deferred-column reload, unrelated
  to GFKs). This hits nearly every query in this package —
  `device_list`/`interface_list`/`virtual_machine_list`/`vm_interface_list`/
  `tenant_list` all request `custom_fields`. On the large instance's
  37,021-row `interface_list` fetch alone, that's ~37,000 extra queries
  under the old behavior. Fixed in v4.6.7.
- [**#22837**](https://github.com/netbox-community/netbox/issues/22837) —
  a **to-one** relation (`site`/`role`/`platform`/`device_type` - exactly
  what `deviceListGraphQLbody`/`flatDeviceListGraphQLbody` nest) doesn't
  cost one row for the related object; it costs one row for *every other
  object that shares that same related object*, independent of page size
  or how many rows you actually requested. 1,000 devices at one site costs
  1,000 rows fetched just for resolving `site`. This directly affects both
  `device_list` query variants in this package. Fixed in v4.6.8.
- [**#22877**](https://github.com/netbox-community/netbox/issues/22877) —
  smaller in scope: `CustomFieldManager.get_for_model()`'s cache check
  treated a cached "this model has no custom fields" result as falsy,
  bypassing the cache and re-querying every time. Compounds with #22813.
  Fixed in v4.6.8.

**Confirmed**: the small instance was upgraded 4.5.9 → 4.6.7 → 4.6.8 and
re-benchmarked at each step (same 279 devices / 7,965 interfaces / 1,302
addresses throughout):

| | A (nested) | G (flat, concurrent) | C addresses phase (REST) | F/G addresses phase (`ip_address_list`) |
|---|---|---|---|---|
| 4.5.9 | ~10.15s | ~2.3–2.8s | ~1.9s\* | ~2.7s\* |
| 4.6.7 (has #22813 only) | 8.517s | 3.04s | 2.136s | 2.672s |
| 4.6.8 (has all four) | 7.559s | **1.702s** | 2.295s | **367ms** |

\*4.5.9 figures are single representative runs from the original A–G
benchmark, not a rerun of every variant at each version like 4.6.7/4.6.8
were.

The `ip_address_list` fetch — the exact query #22787 (`assigned_object`)
targeted — dropped **2.672s → 367ms (~7.3x)** going 4.6.7→4.6.8, precisely
the version that issue shipped in: about as clean a confirmation as a real
production instance can give that this was the actual mechanism, not a
coincidence. The nested query (A) also improved with each upgrade
(10.15s→8.517s→7.559s, ~25% total) — consistent with these bugs affecting
both query shapes, as expected. REST (C's addresses/interfaces phases)
**did not improve at all** across any version (interfaces phase: 35s→
37.054s→37.556s) — confirming that slowness is a separate, still-open
issue, unrelated to this bug cluster, and unaffected by upgrading.

Combined effect of this session's client-side rework *and* the Netbox
upgrade, old-nested-on-4.5.9 vs. flat-concurrent-on-4.6.8: **10.15s →
1.702s, ~6x**. **Recommendation**: upgrade any Netbox instance this
package talks to, to ≥v4.6.8 — confirmed, not just theorized, to help both
`GetDevice`/`GetVM` (still on the nested query) and `GetDevices`/`GetVMs`
(the flat path), independent of and in addition to any further
client-side work.

**The large instance confirms this at real scale**: upgraded 4.5.10 →
4.6.8 (1,375 devices / 37,024 interfaces / 3,794 addresses throughout):

| | A (nested) | G (flat, concurrent) | C interfaces phase (REST) |
|---|---|---|---|
| 4.5.x (pre-upgrade, from the original A–G run) | 2m47.7s | 20.2s | 2m39.9s |
| 4.6.8 | **45.38s** | **6.261s** | 2m14.1s |

The Netbox upgrade alone cut the nested query by **~3.7x** (2m47.7s →
45.38s) even with no client-side change at all — consistent with the
small instance, confirming these bugs scale with row count as expected
(bigger inventory, bigger fixed win). On top of the upgraded server, the
flat+concurrent approach is *still* worth **~7.25x** (45.38s → 6.261s) —
so this wasn't just working around soon-to-be-fixed Netbox bugs, there's
a real, independent client-side win underneath. Combined,
old-nested-on-pre-upgrade vs. flat-concurrent-on-4.6.8: **2m47.7s →
6.261s, ~26.8x**. REST stayed just as broken (2m39.9s → 2m14.1s, no
meaningful change) — the same conclusion as the small instance: REST's
interfaces-endpoint slowness is unrelated to this bug cluster and
unaffected by the upgrade.

## Testing

`netboxtool_test.go` covers `GetTenant`'s REST filtered lookup;
`netboxtool_flat_test.go` covers `GetDevices`/`GetVMs`' flat-fetch-and-
stitch path end-to-end (a fake server that routes each GraphQL request by
inspecting its query text) plus `stitchDevices`/`stitchVMs` edge cases.
Both use `httptest.NewServer` — no real Netbox instance needed to run
`go test ./...`.

Anything not covered by those (a real query rejected by an actual Netbox
schema, real-world timing) still means running the CLI — or its
`benchmark`/`introspect-ip-address` subcommands — against a real (or test)
Netbox instance with a valid config file (see
[README.md](README.md#configuration)).
