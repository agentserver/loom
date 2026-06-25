# Commander Playwright e2e against live observer + OAuth login

**Date:** 2026-06-25
**Author:** auto-brainstorm
**Status:** design — ready for review

## Goal

A Playwright end-to-end test that drives the commander web UI against
the **live** observer-server + driver-agent processes (not vite preview
with mocked routes), exercising the full new-session "fresh-id" flow
that PR #33 fixed:

1. Open commander at `http://127.0.0.1:18091/commander/`.
2. Complete the agentserver OAuth device-flow login the first time
   (subsequent runs reuse a cached session cookie).
3. Click `+` on the driver-codex daemon to create a draft pending
   session.
4. Type a prompt; click send.
5. Assert codex actually responds (assistant chunk text appears).
6. Assert the daemon-tree row for the new session shows the real
   codex-minted thread ID, NOT the client-minted placeholder UUID
   that was submitted with `fresh: true`. This is the rebind that
   pr33 fixed; if it regresses, the row stays on the placeholder.

The first-run OAuth step pauses for up to 10 minutes and prints the
`verification_uri_complete` URL to stdout in a copy-pasteable banner;
the human opens it in any browser and authorizes. The resulting
session cookie is persisted to a local file so every subsequent run
is fully headless and fast.

## Non-goals

- Automating the OAuth authorization page itself. Anthropic-style
  OAuth requires a real human review; we don't try to script it.
- Replacing the existing mocked Playwright suite. The existing
  `commander.spec.ts` keeps its full coverage of mobile/tablet
  layouts, drawer overlays, file preview history, etc. against vite
  preview. The live test is a single complementary smoke for the
  fresh-id rebind path.
- Covering claude or opencode backends. Codex is the only shipped
  backend that needs the fresh-id path; other backends are out of
  scope.
- Driving the slave-agent. The driver-only commander view is enough
  to exercise the rebind logic.

## Architecture

Three new files + one config entry. No changes to existing tests or
production code.

```
multi-agent/internal/commanderhub/webapp/
  playwright.live.config.ts          (new — live-target Playwright config)
  src/e2e/
    live-login.ts                     (new — shared login helper)
    commander-live.spec.ts            (new — fresh-id flow assertion)
  package.json                       (add npm script test:e2e:live)
multi-agent/tests/prod_test/
  .playwright/                       (cache dir, already covered by /tests/prod_test/* gitignore)
```

### `playwright.live.config.ts`

Pinned to the live observer. No `webServer` block — assumes observer
+ driver are already running at 18091 / 18092 (the user starts them
manually). A single `chromium-desktop` project; mobile coverage stays
in the existing mocked suite. Wires a `globalSetup` that performs
the cached-or-live login dance and writes the resulting storageState
to a **deterministic cache path** that both the config and the
helper agree on at module load (no `process.env` round-trip:
Playwright evaluates the config BEFORE globalSetup runs, so env vars
set inside globalSetup do not reach `use.storageState`).

Key settings:

- `baseURL: 'http://127.0.0.1:18091'`
- `globalSetup: './src/e2e/live-login.ts'` (helper's default export)
- `use.storageState: STORAGE_STATE_PATH` — a const imported from
  `./src/e2e/live-login.ts`. The webapp's `package.json` has
  `"type": "module"`, so `__dirname` is undefined — the const uses
  the ESM idiom:
  ```ts
  import { fileURLToPath } from 'node:url';
  import { dirname, resolve } from 'node:path';
  const HERE = dirname(fileURLToPath(import.meta.url));
  // src/e2e → src → webapp → commanderhub → internal → multi-agent
  export const STORAGE_STATE_PATH = resolve(
    HERE, '../../../../../tests/prod_test/.playwright/observer-session.json'
  );
  ```
  Resolves to `<repo>/multi-agent/tests/prod_test/.playwright/observer-session.json`.
  globalSetup writes to that same path. Both config and helper
  import the const so they cannot drift.
- `testMatch: 'commander-live.spec.ts'` — pin so the existing
  `commander.spec.ts` mocked suite does NOT get picked up under
  `testDir: './src/e2e'`.
- `timeout: 120_000` — generous for codex first-turn cold start.
- `expect: { timeout: 10_000 }` — generous for daemon-tree refresh
  after rebind (loadTree() runs after `done`, codex can take
  seconds).
- `retries: 0` — flaky e2e against a real backend should fail loudly,
  not retry-mask.
- Single test project: `chromium-desktop` at 1440x960. Same viewport
  as the existing mocked desktop project.

### `live-login.ts` (globalSetup)

Default-export an async function with the Playwright globalSetup
signature `(config: FullConfig) => Promise<void>`. Behavior:

1. Use the exported `STORAGE_STATE_PATH` constant (defined as shown
   in §playwright.live.config.ts above). Both the config and the
   helper import it, so they always resolve to the same on-disk
   file. `mkdir -p` the parent dir on the first write.
2. **TCP probe** `127.0.0.1:18091` and `127.0.0.1:18092` with a
   2-second timeout each. If either is down, throw with a clear
   message telling the user to start observer + driver first. Don't
   bother continuing — every test would fail anyway. The probe only
   confirms ports are open, NOT that the driver-codex daemon is
   registered with observer; that readiness check happens in step
   7 below, after auth.
3. **Validate the cached cookie if present.** If the cache file
   exists AND is parseable JSON, launch a one-off browser context
   with `storageState: <cache path>`, request
   `/api/commander/tree`, and check the response. 200 means the
   cookie still works — skip to step 7. 401 means the cookie
   expired — fall through to step 4. File missing OR
   parse error → also fall through; treat parse errors as cache
   miss, never as a fatal. (No mtime/TTL guessing: we trust the
   server's own answer.)

4. **Live login path:** launch a headed browser (so the user can
   visually confirm the page loaded), navigate to
   `http://127.0.0.1:18091/commander/`. Attach the response waiter
   BEFORE clicking to avoid a race:

   ```ts
   const [loginResp] = await Promise.all([
     page.waitForResponse('**/api/commander/login'),
     page.getByRole('button', { name: '用 agentserver 登录' }).click(),
   ]);
   const body = await loginResp.json();
   const verifyURL = body.verification_uri_complete as string;
   ```

   Print to stdout in a banner:

   ```
   ╔══════════════════════════════════════════════════════════════╗
   ║  OPEN THIS URL TO AUTHORIZE COMMANDER:                       ║
   ║                                                              ║
   ║  <verification_uri_complete>                                 ║
   ║                                                              ║
   ║  Waiting up to 10 minutes...                                 ║
   ╚══════════════════════════════════════════════════════════════╝
   ```

   The headed browser also shows the verifyURL link in-app (the
   webapp renders an anchor); both routes are fine.

5. **Wait for login to complete.** Wait up to 10 minutes for the
   commander tree to appear — selector
   `getByTestId('daemon-tree')`. The webapp's poll loop swaps the
   login UI for the tree when the cookie lands. As soon as it
   appears, save `storageState` to `STORAGE_STATE_PATH`.

   ```ts
   await expect(page.getByTestId('daemon-tree')).toBeVisible({
     timeout: 600_000,
   });
   // Atomic write: storageState → temp file → rename. Prevents
   // partial JSON if globalSetup is killed mid-save.
   const tmp = STORAGE_STATE_PATH + '.tmp';
   await context.storageState({ path: tmp });
   fs.renameSync(tmp, STORAGE_STATE_PATH);
   ```

6. Close the browser. The config already points
   `use.storageState` at `STORAGE_STATE_PATH`, so the spec picks up
   the cookie automatically.

7. **Daemon readiness poll.** Before returning from globalSetup,
   open a fresh API request context with the storageState and poll
   `GET /api/commander/tree` until the response contains a daemon
   with `kind: 'codex'` and `status: 'ok'`. Up to 30s, 1s interval.
   If timed out, throw with a clear message (`driver-codex daemon
   not registered with observer — check driver logs`). This
   prevents the spec from racing the daemon-link handshake.

Cookie clearance / re-login is manual: the user deletes
`tests/prod_test/.playwright/observer-session.json` and runs again.
No UI affordance — this is a developer test, not a product flow.

### `commander-live.spec.ts`

A single test:

```
test('fresh + button creates session and rebinds to real codex ID', async ({ page }) => { ... })
```

Steps:

1. Navigate to `/commander/`. The storageState already has the
   session cookie from globalSetup, so the daemon tree loads
   directly — no login flow.

2. Locate the driver-codex daemon's `+` button. Click it. Assert a
   draft pending session row appears with a UUID-shaped
   `data-session-id` attribute. Capture that placeholder ID into a
   variable.

3. Pre-attach a `page.waitForRequest('**/turn')` waiter and a
   `page.waitForResponse('**/turn')` waiter (the turn POST returns
   an SSE stream). Type a short prompt ("say hi") into the composer.
   Click send. Resolve the request waiter; parse its `postData()`
   JSON; assert `fresh === true`. This is the only assertion that
   proves the frontend flagged the first turn correctly.

4. Resolve the SSE response. Read its body in chunks; parse the
   final `event: done\ndata: {...}\n\n` frame. Extract
   `data.result.session_id` — this is the REAL backend-minted ID
   the test will pin to. Save it as `realID`.

5. Assert `realID` is a well-formed UUID **and not equal to** the
   placeholder captured in step 2. (Catches both regressions:
   backend didn't mint a fresh ID, or backend echoed the
   placeholder.)

6. **Pre-arm the detail-fetch waiter NOW, before any tree
   assertion.** The frontend's [selected, tree, pendingSession]
   effect can fire the `GET /api/commander/daemons/<daemonID>/sessions/<realID>`
   immediately after rebind (before Playwright even gets a chance
   to inspect the DOM). Attaching the waiter after the tree-row
   assertion would race:

   ```ts
   const detailPromise = page.waitForResponse(
     (resp) =>
       resp.url().endsWith(`/sessions/${realID}`)
       && resp.request().method() === 'GET'
       && resp.status() === 200,
     { timeout: 30_000 },
   );
   ```

7. Wait up to 30 seconds for the daemon-tree to refresh and contain
   a session row whose `data-session-id` exactly equals `realID`.
   This proves the frontend's loadTree() saw the new row AND the
   placeholder row was rebound (not stacked).

8. `await detailPromise`. This proves `selected` was rebound to the
   real ID and the detail effect fired against the correct URL —
   the assertion that step 6's pre-armed waiter actually saw a
   matching request.

If any of these fail, the fresh-id rebind has regressed. Step 5 +
step 6 together are the load-bearing assertion — pre-pr33, the
backend 32600'd with "no rollout found" so step 4 would time out;
post-pr33 but with a broken hub rekey, step 6 would fail because
the tree never shows the real ID under the daemon. The two-step
"capture exact ID from SSE, then assert exact ID present in tree"
shape eliminates false-positives from any stale codex sessions that
might exist on this driver.

### Frontend changes (minimal)

The only production code change is adding `data-testid`/`data-*`
attributes the test needs:

- `data-testid="daemon-tree"` on the daemon-tree root element
  (already may be `.daemon-tree` className — adding the testid lets
  the test bind without depending on layout class names).
- `data-session-id={session.session_id}` on each rendered session
  row (both pending and real). Necessary to capture the placeholder
  and assert the rebound ID afterwards.

These are stable hooks and don't add styling. They make the test
much less brittle than text-matching.

If the existing mocked spec has its own assumptions, the new
attributes are additive — no existing assertions break.

## Data flow / sequence

```
                              first run                         every other run
                              ────────                          ───────────────
globalSetup                   probe :18091, :18092              probe :18091, :18092
                              cookie file missing/invalid       cookie file valid
                              launch headed browser             launch headless w/ storageState
                              click 用 agentserver 登录         GET /api/commander/tree → 200
                              POST /api/commander/login
                              read verification_uri_complete
                              PRINT BANNER to stdout
                              wait 10 min for tree visible
                              user opens URL, authorizes
                              webapp poll receives ok, tree
                                renders
                              atomic write storageState →       (no write — cookie still valid)
                                STORAGE_STATE_PATH (.tmp+rename)
                              close browser
                              poll GET /api/commander/tree      poll GET /api/commander/tree
                                until codex daemon status: ok     until codex daemon status: ok

spec runs (same in both):
                              browser context auto-loads storageState from STORAGE_STATE_PATH
                                (config.use.storageState resolves to that const at config load)
                              navigate /commander/ → tree visible immediately
                              click + on driver-codex
                              capture placeholder data-session-id
                              type "say hi"
                              attach POST .../turn request+response waiters
                              click send
                              resolve POST request → assert body.fresh === true
                              resolve POST response (SSE) → read done frame
                                → realID = data.result.session_id
                              assert realID is UUID && realID !== placeholder
                              pre-arm GET /sessions/<realID> response waiter
                              wait for tree to contain row with data-session-id === realID
                              await detailPromise
                              done
```

## Error handling

- **Observer/driver down:** TCP probe in globalSetup throws with the
  command to start them. Test suite exits with a non-zero code.
- **Cached cookie expired:** /api/commander/tree returns 401 →
  globalSetup falls through to live login automatically. No retry
  loop, no special case.
- **OAuth not completed in 10 min:** the
  `getByTestId('daemon-tree').toBeVisible({ timeout: 600_000 })`
  call fails. Globalsetup throws, test suite exits non-zero. Cache
  file is NOT written (the user can retry).
- **POST /api/commander/login fails (e.g., agentserver 502):**
  globalSetup throws with the error body. Same as before.
- **Codex doesn't respond within 60s in the spec:** test fails at
  step 5 with a chunk-not-visible timeout. Either codex is slow or
  the fresh-id routing is broken.
- **Tree row never rebinds:** test fails at step 6. This is the
  exact regression we're guarding against.

## Testing this test

- Smoke: from cold (no cache file), run once, complete OAuth,
  confirm cache file is created at the expected path and the test
  passes.
- Speed-of-second-run: run again immediately, confirm globalSetup
  takes <2s (no banner printed) and the test reuses the cookie.
- Cookie invalidation: delete the cache file, run again, confirm
  the banner reappears and a fresh OAuth dance works.
- Failure visibility: stop the driver, run the test, confirm the
  TCP probe error message is the first thing the user sees (not a
  cryptic Playwright timeout).

## Risks

- **OAuth re-auth nag:** if the agentserver-issued session cookie
  has a shorter-than-expected TTL, the user gets the banner every
  few hours. Mitigated by the "always try the cookie, fall back on
  401" model — we don't try to predict TTL.
- **Test depends on a real codex respond:** if codex is rate-limited
  or the agentserver is down, the test fails. That's the correct
  behavior for a live e2e — failure here points at the actual
  system, not the test.
- **`data-session-id` attribute name collision:** unlikely; current
  webapp uses className-based queries. Verify at implementation
  time that no other DOM element claims that attribute.
- **storageState file contains a sensitive cookie:** explicitly
  gitignored. Path is under `tests/prod_test/.playwright/` which is
  already a test-artifact area. Document in the spec header that
  this file must NOT be committed.

## Files (summary)

| File | Change |
|---|---|
| `multi-agent/internal/commanderhub/webapp/playwright.live.config.ts` | new — live-target Playwright config; no webServer; chromium-desktop only; globalSetup wired |
| `multi-agent/internal/commanderhub/webapp/src/e2e/live-login.ts` | new — globalSetup helper: TCP probe, cookie validate, live login w/ printed banner, storageState save |
| `multi-agent/internal/commanderhub/webapp/src/e2e/commander-live.spec.ts` | new — single test exercising the fresh-id rebind |
| `multi-agent/internal/commanderhub/webapp/src/CommanderApp.tsx` or `DaemonSessionTree.tsx` | add `data-testid="daemon-tree"` to the tree root; `data-session-id={session.session_id}` to each row |
| `multi-agent/internal/commanderhub/webapp/package.json` | add `"test:e2e:live": "playwright test --config=playwright.live.config.ts"` |
| `.gitignore` | no change needed — existing `/tests/prod_test/*` pattern already covers `.playwright/` |
