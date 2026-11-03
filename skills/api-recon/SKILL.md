---
name: api-recon
description: Invoke this skill when collecting a website's API endpoints.
---

# API Recon (front-end interface reconnaissance)

Under an **authorized** engagement, discover as completely as possible: the **back-end API** (paths, methods, parameters, response bodies), **front-end routes**, and **UI feature trigger points** (tabs, dialogs, table actions, etc.).

---

## Boundaries and prohibitions (Agent must-read — crossing them is out of scope)

This skill performs **API / parameter-surface reconnaissance only**; it is not a vulnerability-hunting or exploitation phase.

### Task boundaries

| Scope | Allowed | Prohibited |
|---|---|---|
| **Goal** | Enumerate paths, methods, parameters, routes, UI trigger points | SQLi/XSS/privilege-escalation/brute-force/fuzz vulnerabilities, packet-tampering attacks, destructive operations |
| **Authentication** | Hook + stub/mock to bypass the **client-side** login gate | Asking the user for or guessing usernames/passwords; attempting a real login-form submission |
| **Runtime** | Hook endpoints without credentials and use mock responses to drive the SPA into its logged-in shell | Flows that cannot continue without a real back-end session |

### Credential-free dynamic analysis (Phase 3 default)

1. Use `preload.js` / `runtime_harvest.js` to **intercept and stub** bootstrap endpoints such as login, permissions, and menus;
2. For business query endpoints, return a mock body that is **structurally correct, carries a success business code, and may hold empty data**;
3. Let the front-end still render logged-in pages with no back-end or under a 401, so that more XHR/fetch/WebSocket calls are triggered;
4. **Empty data, blank tables, and placeholder UI are all expected**—do not switch to a real login or vulnerability testing because of them.

**In one line**: use mocks to prop open the front-end routes and component mounts, and **record outbound requests only**; what the back-end returns does not matter — what matters is **which other endpoints the front-end will still call**.

### Hard process prohibitions

| Prohibited | Alternative |
|---|---|
| grep/curl/Read the main entry `index-*.js` to extract API paths before Phase 1 is done | Run `OUTDIR/harvest_static.py` |
| Hand-writing scripts like `extract_apis.py` that replace harvest | Edit `OUTDIR/harvest_static.py` and rerun |
| Repeating the same grep/command after it has failed ≥2 times | Change strategy: read tool_logs, edit harvest, check reference |
| Skipping gate A/B and running the original `scripts/` directly | Copy to OUTDIR and adapt to the target |
| Real username/password, OTP, OAuth, or other authentication | stub/mock (see above) |
| Skipping the stub "to get real data" and doing privilege-escalation/injection testing | Record outbound only — that is the recon boundary |
| Irreversible operations such as delete, exporting sensitive data, or bulk writes | Same applies to coverage clicks |
| Claiming all pages and endpoints are captured without completing runtime + dynamic enumeration | See "Definition of done" or note the limitation |
| Claiming all parameters are known without completing the parameter-trigger matrix + diff | Phase 3b matrix + Phase 5 diff |
| Inferring required/optional from a single runtime sample | Multi-sample diff, or validation rules / error-based inference |

---

## Two-layer model + run modes

| Layer | Output | Ceiling |
|---|---|---|
| **Static** (JS bundle) | Full endpoint paths, draft route list, payload-assembly field candidates | No HTTP methods; parameters need Phase 1b; misses runtime-concatenated URLs |
| **Runtime** (live session) | Method + body + response + dynamic URLs + WS/SSE; multi-sample diff fills in parameters | A page must actually render to issue requests; a single sample is not enough to decide required/optional |

| Run mode | Engine | Use for |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/response body, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Click tabs/dialogs/tables for deeper feature-point coverage |
| **both** | depth first, then coverage | Most complete, longest-running |

**Parameter methodology** (no generic script): derive paths via harvest/regex; derive parameters via **anchor-window expansion + UI binding chain + multi-sample diff + error-based inference** (grep recipes in section J of [reference.md](reference.md)).

---

## Definition of done

recon may be declared complete only when all of the following hold:

- [ ] **Static**: Phase 1 harvest produced `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: at least one of depth or coverage; coverage/both require a **working hook + dynamic enumeration loop**
- [ ] **Entered the shell**: visiting a business path does not land on `/login` (mind hash routes)
- [ ] **Parameters**: coverage/both completed the parameter-trigger matrix + `param_samples.json`; Phase 5 merged into `params_merged.json`
- [ ] **Depth** (if module pages are blank): Phase 4 permission-tree reconstruction and rerun, until **module-level APIs** appear (not just locale/bootstrap)
- [ ] **Delivery**: Phase 5 outputs complete (see Phase 5 output table); `insert_assets` has written the service and endpoint assets

---

## Scripts and gates

`scripts/` are reference templates only; running the originals verbatim and treating that as the final result is **prohibited**.

**Rule**: read first → adapt to the target → write into `OUTDIR` (e.g. `recon/`) → log in `CHANGES.md`; if they do not fit, rewrite following the methodology, borrowing only the structure.

| Gate | When | Reference script → OUTDIR copy | Common must-edits |
|---|---|---|---|
| **A (static)** | After Phase 0, before the **first** harvest/spider run | `harvest_static.py` / `spider_mpa.py` | **The default regex runs as-is on most sites**; only when the manifest/dialect does not match, edit the endpoint regex, webpack/Vite `publicPath`, MPA exclude/cookie |
| **B (runtime)** | After Phase 2, before running depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success value, stubs, login regex, api prefix, hash/history |

**Mandatory SPA order** (not interchangeable; Phase numbering takes precedence over "explore first, then script"):

| Step | Must | Must not |
|---|---|---|
| After Phase 0 | Next Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read the main entry `index-*.js` (usually >500KB) |
| Gate A | Copy script → small edits as needed → **run immediately** | Manually extract APIs first, then decide whether to harvest |
| Before Phase 1 is done | Validate output with `wc -l`; on 404, edit harvest and retry | Hand-written extract scripts; repeatedly grepping a URL not yet downloaded |
| From Phase 1b | grep only `OUTDIR/js/*.js` | Use the main bundle instead of harvest |

- ✅ Copy `harvest_static.py` → (optional) edit regex → **run immediately**
- ❌ curl the main bundle → grep many times → write a temporary extract → harvest only at the end
- **MPA**: after Phase 0, the next Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## Tool and output constraints

| Constraint | Detail |
|---|---|
| Large files | `index-*.js` >100KB **must not** be Read/grepped into context; batch-process with OUTDIR scripts |
| grep output | Always `\| head -20` or `-m 5`; keep only a path summary in the conversation, do not paste bundle fragments |
| Validation | Use `wc -l`, `ls \| wc -l`; do not Read whole directories |
| regex probe | Optional, ≤1 time, only on ≤50KB small chunks or HTML; the authoritative static source is harvest |
| reference | Recipes/templates/troubleshooting are in [reference.md](reference.md); do not inline the full text again |

---

## Execution roadmap

```
Phase 0 classification + OUTDIR
  → Gate A → Phase 1 harvest (★ run immediately ★)
  → Phase 1b parameter reverse-engineering
  → Phase 2 three authentication gates → config.json
  → Gate B → Phase 3 runtime + parameter matrix
  → Phase 4 permission tree (when needed) → rerun Phase 3
  → Phase 5 merge report + insert_assets bulk-inserts every discovered service and endpoint API asset; in no case may a discovered asset be omitted on insertion
```

Check off in order; **you may not enter the next Phase until the previous item is done**.

1. [ ] **Phase 0**: initial SPA/MPA probe; create `OUTDIR` → [Phase 0](#phase-0--classification)
2. [ ] **Gate A + Phase 1**: copy script → harvest **immediately** → `wc -l` validation → [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: anchor-window expansion + binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--parameter-reverse-engineering)
4. [ ] **Phase 2**: three authentication gates → `config.json` → [Phase 2](#phase-2--three-authentication-gates)
5. [ ] **Gate B**: adjust the runtime script → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm you entered the shell; parameter-trigger matrix → `param_samples.json`
7. [ ] **Phase 4** (if needed): permission tree → patch stubs → rerun Phase 3 → [Phase 4](#phase-4--permission-tree-reconstruction)
8. [ ] **Phase 5**: merge outputs + report + `insert_assets` → [Phase 5](#phase-5--merge-and-report)

---

## Phase 0 — Classification

Fetch the entry HTML and **create `OUTDIR`** (do not edit `scripts/` inside the skill):

- **SPA**: empty shell + `<div id=app>` + chunks → Phase 1–5
- **MPA**: SSR + `<form>`, no endpoint bundle → after Gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Produces `forms.txt`, `links.txt`, `api_inline.txt`. For an SPA, if forms ≈ 0 → switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts and gates](#scripts-and-gates) · [Tool and output constraints](#tool-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse HTML scripts → webpack/Vite manifest → download all lazy chunks → produce `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Chunk count vs manifest: on a 404, edit harvest and retry; do not curl chunks one by one by hand
- `api_static.txt` too small → relax the endpoint regex in OUTDIR and rerun (see reference)

### Phase 1b — Parameter reverse-engineering

Paths come from Phase 1; parameter fields need their own recon. grep rules in [Tool and output constraints](#tool-and-output-constraints).

**Completion bar**: for the important endpoints you can answer—field name, transport location, type inference, whether required, sample value, confidence.

#### 1b.0 — Transport shape

| Shape | Where the parameters are | Look at first in static |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` next to the path anchor |
| GraphQL | `variables` | gql templates, `$page: Int` |
| Classic form | urlencoded | `<form>`, `FormData` |
| File upload | multipart | `FormData.append` |
| Path parameter | `/user/:id` | route table + `useParams` / `$route.params` |
| Encrypted/signed | wrapped into `sign`/`data` | Hook the encryption function's arguments (reference section D) |

Output: tag every endpoint with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor-window expansion

Anchor on a known path and widen the window to find the payload-assembly object:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper layer | Parameter clues |
|---|---|
| axios instance | `data` / `params` |
| Unified request | interceptor injects global fields |
| OpenAPI client | generated method signatures |
| React Query / SWR | hook's second argument |
| Vue composable | composable arguments |

Type residue: `yup`/`zod`/rules, `Form.Item name=`, embedded Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding source | Technique |
|---|---|
| Form submit | follow submit → transform → API |
| Table search | `getFieldsValue()` → `params` |
| Route | `:id` / `?tab=` |
| Interceptor | global `tenantId`, pagination, sign |
| Enum select | `options` → API enum values |

In the DevTools call stack, trace upward from `fetch`/`XHR.send` to the payload-assembly function.

#### 1b.3 — Three payload-assembly questions (≠ the Phase 2 auth gates)

| Question | What to answer |
|---|---|
| **Assembly** | where the payload is built, transform traces |
| **Validation** | required, pattern, enum |
| **Transport** | path / query / body / multipart / headers |

At the interceptor gate (Phase 2), also read the globally injected fields (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Hand-off to Phase 3

Candidate fields come from the static/binding layer; **required/optional/conditional dependencies** require the Phase 3 parameter matrix + diff + Phase 5 error-based inference.

---

## Phase 2 — Three authentication gates

grep in `OUTDIR/js/` (with `head`) and write into `config.json` (recipes in reference):

| Gate | Question | Keywords |
|---|---|---|
| **Render gate** | How is logged-in state decided? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What triggers a redirect to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do the menu/permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Do not treat a localStorage key name as a credential—confirm it from the chunk/request chain.

**Exit = Gate B**: land the conclusions in `config.json` and edit `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API observation (optional)

Use `preload.js` in OUTDIR to confirm session key names, Authorization, and nested API URLs:

| Config | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | header observation |
| `extractUrlsFromResponse: true` | sub-APIs inside responses |
| `observe.storageReads/cookieReads: true` | back-fill config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage exports each round: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Must have passed Gate B; follow [Boundaries and prohibitions](#boundaries-and-prohibitions-agent-must-read--crossing-them-is-out-of-scope) · the credential-free mock strategy.

Set `"runtimeMode": "depth" | "coverage" | "both"` in `config.json` (template in reference).

### Hooks and stubs (shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 exact | stub auth/permissions/bootstrap | pass the first-screen authentication |
| L2 negative correction | all JSON responses | not-logged-in code → success |
| L3 fallback | `/api` etc. not matched by L1 | empty success body, prop open the UI |

- **depth**: fake auth + `forward` rewriting the business code + `stubs`; traverse `routes` (hash/history); produce `runtime_api.json`
- **coverage**: inject `preload.js` at **document-start** (CDP `addScriptToEvaluateOnNewDocument` or a Userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; a business path does not bounce back to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage dynamic enumeration (required)

1. Main nav/sidebar — click each item, wait 1–3s for the network
2. Tabs — `role=tab`, `.ant-tabs-tab`
3. Tables — view/edit/detail on the first row
4. Toolbars — export, filter, new (**avoid irreversible deletes**)
5. On entering each module — merge APIs/routes
6. SPA — controlled `pushState` for paths not covered by `routes.txt` (prohibited for MPA)

**Parameter-trigger matrix** (required): record once per operation type in each module, then **diff multiple samples**:

| Operation | Parameters it usually adds |
|---|---|
| List first screen | pagination + default filter |
| Click search | keyword, filter |
| Advanced filter | more optional ones |
| New/edit | the full entity |
| Bulk/export/sort | `ids[]`, `exportType`, `sortField` |

**Under a stub the outbound body/headers are still real**—go by the request. Record → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, then 3b coverage

---

## Phase 4 — Permission-tree reconstruction

**Trigger**: blank module pages / each route only loads bootstrap (e.g. locale) → the content gate was not passed.

| Symptom | Meaning |
|---|---|
| Entered the shell | render gate + interceptor gate passed |
| Sidebar items missing / clicks blank | stub shape or permission codes incomplete |
| Same, very few APIs per route | `v-if permission` did not pass |
| `routes.txt` far smaller than the bundle | must be completed from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate outputs: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

stub check: the outer `response_code` matches the interceptor gate; flat codes align with the tree; `routes` cover every link in `route_map`.

After updating `config.json`, **rerun Phase 3**. For a large SPA you can tune `waitUntil`, `routeTimeout`, `perRouteMs` (see reference sections A3/I).

---

## Phase 5 — Merge and report

### Output table

| File | Phase | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | static bundle and paths |
| `param_candidates.json` | 1b | static parameter-field candidates |
| `config.json` | 2 | three gates + runtime config |
| `runtime_api.json` | 3a | detailed depth recording (incl. WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | multi-sample, click log, detail |
| `route_map.json` etc. | 4 | permission-tree intermediate files (if run) |
| `params_merged.json` | 5 | merged parameter fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | routes, APIs, params, feature points, limitations |
| **insert_assets** | 5 | write every service and endpoint asset into the asset store |

### 5b — Parameter merge

diff from `param_samples.json`; **no generic merge script**. Confidence rules in reference J7 (high/medium/low/pending-trigger).

### 5c — Error-based inference

Within the authorized scope you may send incomplete requests to read 400s (**this is parameter recon, not vulnerability testing**): `field 'x' is required`, enum errors, etc. Mind the `data` wrapper, `variables`, and `bizData` before encryption.

The report must state: runtimeMode, static/runtime API counts, parameter confidence, uncovered modules, and a summary of `CHANGES.md` relative to the reference scripts.

Suggested `site_map.json` structure:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

More fields and grep recipes are in [reference.md](reference.md).

---

## General notes

- **Framework-agnostic**: the method is the same for webpack/Vite/Angular lazy load
- **Transport**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is out of scope
- **SSR**: client-side fetch is recordable; RSC/Server Actions are not fully enumerable
- **Blind spots**: JSVMP, WASM, strong HMAC/mTLS validation → static + note the limitation
- **Parameter blind spots**: conditional interdependence, hidden params, WASM payload assembly → "pending-trigger" / "unreachable"
- **Static is the safety net**: when runtime is blocked, static can still enumerate endpoints

---

## Additional resources

- Grep recipes, `config.json` template, troubleshooting, hooks, parameter reverse-engineering section J, site_map template: **[reference.md](reference.md)**
- Reference-script paths are in the [Scripts and gates](#scripts-and-gates) table
