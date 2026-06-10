# gstack Cookie Import vs agent-browser

Date: 2026-06-10
Status: Investigation note

## Summary

gstack has a real local-browser cookie importer. It discovers installed
Chromium-family browsers, reads their profile cookie databases, decrypts cookie
values through the host platform's browser credential mechanism, and injects a
selected set of cookies into gstack's persistent Playwright context.

`agent-browser`, as packaged in Oro today, does not document an equivalent local
browser cookie import/decryption path. Its auth model is: log in through the
agent browser, save/load its own state files, manually set cookies, or attach to
an already-debuggable Chrome/CDP endpoint. That is materially different from
"take my normal Chrome/Comet/Brave cookies and copy selected domains into the
agent's browser context."

For Oro browser skills, gstack is the better reference for local cookie import
mechanics. `agent-browser` remains useful as a browser automation backend, but
Oro would need its own explicit, scoped auth-bundle/import layer if we want
gstack-like local-cookie ergonomics.

## Sources Read

gstack reference files:

- `archive/yap/reference/gstack/setup-browser-cookies/SKILL.md`
- `archive/yap/reference/gstack/browse/src/cookie-import-browser.ts`
- `archive/yap/reference/gstack/browse/src/cookie-picker-routes.ts`
- `archive/yap/reference/gstack/browse/src/write-commands.ts`
- `archive/yap/reference/gstack/browse/src/read-commands.ts`
- `archive/yap/reference/gstack/browse/src/browser-manager.ts`
- `archive/yap/reference/gstack/browse/src/meta-commands.ts`
- `archive/yap/reference/gstack/browse/test/cookie-import-browser.test.ts`
- `archive/yap/reference/gstack/BROWSER.md`
- `archive/yap/reference/gstack/ARCHITECTURE.md`

Oro-packaged `agent-browser` files:

- `cmd/oro/_assets/skills/agent-browser/SKILL.md`
- `cmd/oro/_assets/skills/agent-browser/references/authentication.md`
- `cmd/oro/_assets/skills/agent-browser/references/session-management.md`
- `cmd/oro/_assets/skills/agent-browser/references/commands.md`

Related Oro context:

- `docs/plans/2026-06-10-browser-skills-deepspec.md`
- `docs/research/2026-03-23-gstack-skill-analysis.md`

## How gstack Gets Cookies From Other Browsers

gstack's user-facing skill is `setup-browser-cookies`. It imports cookies from a
real Chromium browser into the headless `browse` session and opens an
interactive picker when the user wants to select domains manually.

The command surface is in `write-commands.ts`:

- `browse cookie-import <json-file>` imports a cookie JSON file into the current
  Playwright context.
- `browse cookie-import-browser <browser> --domain <domain> [--profile <name>]`
  directly imports cookies for a single domain.
- `browse cookie-import-browser <browser> --all [--profile <name>]` imports all
  non-expired domains as an explicit opt-in.
- `browse cookie-import-browser [browser]` opens the local picker UI at
  `/cookie-picker?code=<one-time-code>`.

The browser list is hardcoded in `cookie-import-browser.ts`, not user supplied
as arbitrary paths. The current registry covers Chromium-family browsers:

- Comet
- Chrome
- Chromium
- Arc
- Brave
- Edge

The importer works against browser profiles, usually `Default` or `Profile N`.
`listProfiles()` reads profile display names from each profile's `Preferences`
file and prefers the account email when available. `listDomains()` reads the
cookie SQLite database and returns domain/count metadata before decrypting any
cookie values.

The code is not a Safari/Firefox importer. It is specifically shaped around
Chromium's `Cookies` SQLite database and Chromium encryption formats.

## Decryption Pipeline

gstack reads the Chromium cookie database in read-only mode. If the profile DB
is locked or on Windows, it copies `Cookies`, `Cookies-wal`, and `Cookies-shm`
to a temp directory and opens the copy. It then converts rows into Playwright
cookie objects and calls `page.context().addCookies(...)`.

Platform details from `cookie-import-browser.ts` and its tests:

- macOS `v10` cookies:
  - Reads the browser password from macOS Keychain with
    `security find-generic-password`.
  - Derives an AES key using PBKDF2 with salt `saltysalt`, 1003 iterations, and
    SHA-1.
  - Decrypts Chromium's AES-128-CBC payload with IV of 16 space bytes.

- Linux `v10` cookies:
  - Uses Chromium's historical `peanuts` password.
  - Derives a PBKDF2 key with salt `saltysalt`, 1 iteration, and SHA-1.

- Linux `v11` cookies:
  - Looks up a browser secret via `secret-tool`.
  - Supports Chrome libsecret schema lookups keyed by browser application.
  - Derives the AES-128-CBC key with the same Linux PBKDF2 shape.

- Windows `v10` cookies:
  - Reads `Local State`.
  - Extracts `os_crypt.encrypted_key`, removes the `DPAPI` prefix, and decrypts
    the key through PowerShell/.NET `ProtectedData.Unprotect`.
  - Decrypts cookie values as AES-256-GCM.

- Windows `v20` cookies:
  - Throws a `v20_encryption` error in direct decryption because App-Bound
    Encryption requires the browser process.
  - Falls back to `importCookiesViaCdp()` when all selected cookies fail and
    `hasV20Cookies(...)` detects v20 values.
  - Launches Chrome or Edge headless against the real user-data-dir/profile with
    a randomized local `--remote-debugging-port`, calls
    `Network.getAllCookies`, filters to selected domains, and kills Chrome in a
    `finally` block.

For macOS/Linux CBC cookies, Chromium stores 32 bytes of metadata/HMAC-like
prefix before the actual cookie value. gstack removes those 32 bytes after
decryption and treats the remainder as the cookie value. If the database row has
a plaintext `value` and empty `encrypted_value`, gstack uses the plaintext value
directly.

There is important documentation drift: `ARCHITECTURE.md` still says "No
Windows/Linux cookie decryption. macOS Keychain only" in an intentional
non-goal section, but the current implementation and tests include Linux and
Windows paths, plus the Windows v20 CDP fallback.

## Picker and Import Management

The picker is served from gstack's local browser daemon:

- `/cookie-picker` accepts a short-lived one-time code and sets a
  `gstack_picker` session cookie.
- `/cookie-picker/browsers` returns installed browsers.
- `/cookie-picker/profiles` returns profiles for the selected browser.
- `/cookie-picker/domains` returns domain counts.
- `/cookie-picker/import` decrypts selected domains and imports them into the
  active Playwright context.
- `/cookie-picker/remove` clears cookies for selected imported domains.
- `/cookie-picker/imported` returns currently imported domains and counts.

The picker auth model is deliberately separate from the main command bearer
token. The one-time picker code has a short TTL, and the picker session cookie is
only valid for picker routes, not for `/command`.

Once imported, gstack tracks imported cookie domains in `BrowserManager`. The
read command layer uses that state to block arbitrary JavaScript execution on
pages whose host does not match an imported domain. That is meant to reduce
cross-origin cookie exfiltration after importing sensitive cookies.

## gstack Security Posture

Observed guardrails:

- Browser names and locations are a fixed registry, not arbitrary shell input.
- Profile names reject path traversal, slashes, backslashes, and control chars.
- SQLite reads are read-only or made against temp copies.
- The real browser cookie DB is not modified.
- Keychain, secret-tool, and DPAPI access happen through fixed command argument
  arrays, not string-concatenated shell commands.
- Cookie values are not displayed in the picker.
- The `cookies` read command redacts/truncates values.
- Key material is cached only in memory for the daemon lifetime.
- Direct `--domain` imports must match the current page hostname.
- `--all` exists but requires explicit opt-in and warns the user.
- State save warns that cookies are written in plaintext.

Important residual risks:

- Windows v20 fallback launches a local CDP TCP port. The code comments state
  that a same-user local process could connect before gstack kills the browser
  and exfiltrate decrypted v20 cookies. The noted hardening direction is
  `--remote-debugging-pipe`.
- Imported cookies live in the active Playwright context and can later be saved
  to a plaintext state file if the user runs `state save`.
- The gstack reference implementation is Chromium-specific. Supporting Firefox
  or Safari would require separate storage and platform decryption work.

## How agent-browser Differs

The Oro-packaged `agent-browser` docs describe authentication as browser
automation and state persistence, not native cookie extraction from installed
browsers.

Documented auth paths:

- Drive the login form with `open`, `snapshot`, `fill`, `click`, and `wait`.
- Save auth state with `agent-browser state save auth.json`.
- Restore auth state with `agent-browser state load auth.json`.
- Handle OAuth/SSO and 2FA by navigating the flow, often in headed mode, then
  saving state.
- Manually set a cookie with `agent-browser cookies set name value`.
- Use named sessions for isolated cookies, localStorage, sessionStorage,
  IndexedDB, cache, history, and tabs.
- Optionally auto-save/restore session state by session name.
- Optionally encrypt saved state at rest with `AGENT_BROWSER_ENCRYPTION_KEY`.
- Attach to an existing Chrome endpoint with `--cdp <port>` or use
  `--auto-connect`.

What is not documented in `agent-browser`:

- Installed browser discovery.
- Chrome/Comet/Brave/Arc/Edge profile enumeration.
- Reading Chromium `Cookies` SQLite databases.
- macOS Keychain, Linux secret-tool, or Windows DPAPI cookie decryption.
- Domain/count picker before decryption.
- Selective import from a user's normal browser into a separate Playwright
  context.
- Windows v20 App-Bound Encryption fallback.
- Imported-domain tracking with JavaScript execution restrictions.

The CDP distinction matters. gstack's normal path reads native browser storage
and copies selected cookies into its own context. Its Windows v20 fallback uses
CDP as a decryption workaround. `agent-browser --cdp` is different: it attaches
to a browser already exposed through DevTools and drives that browser/session
directly. That can reuse cookies if the attached browser has them, but it is not
an importer and does not create a scoped copied auth bundle by itself.

## Comparison Table

| Capability | gstack | agent-browser in Oro |
| --- | --- | --- |
| Local installed-browser cookie import | Yes, Chromium-family registry | Not documented |
| Profile discovery | Yes | Not documented |
| Domain picker before import | Yes | Not documented |
| macOS Keychain decryption | Yes | Not documented |
| Linux secret-tool/peanuts decryption | Yes | Not documented |
| Windows DPAPI decryption | Yes | Not documented |
| Windows v20 CDP fallback | Yes | CDP attach exists, but not as cookie-import fallback |
| Manual cookie set | Yes via cookie commands/import JSON | Yes via `cookies set` |
| Save/load agent state | Yes, but state save warns plaintext cookies | Yes, documented; optional encryption env var |
| Persistent browser daemon | Yes, central to `browse` | CLI/session model in docs |
| Imported-domain exfiltration guard | Yes for JS execution | Not documented |
| Non-Chromium import | Not observed | Not documented |

## Implications for Oro

Oro should not assume `agent-browser` already solves "take local cookies from my
real browser." It solves browser driving and state reuse, and it may be a good
backend for v1 browser skills, but local cookie import needs an Oro-owned policy
and UX layer.

Recommended shape for Oro:

- Treat gstack's importer as the reference implementation pattern, not as a
  drop-in product dependency.
- Import into explicit Oro auth bundles, not directly into arbitrary worker
  browser contexts.
- Scope bundles by project, app/environment, browser, profile, and allowed
  domains.
- Show domain/count metadata before decrypting values.
- Require explicit user action for import and especially for all-domain import.
- Never expose cookie values in prompts, reports, logs, or picker UI.
- Prefer copied state over live browser control for repeatable worker runs.
- If implementing a Windows v20 fallback, avoid CDP TCP ports if possible and
  prefer a pipe transport.
- Keep deterministic E2E gates separate from browser skills; browser skills
  should produce evidence artifacts and reusable exploratory flows.

The existing browser-skills deepspec already says local cookie import should be
explicit, scoped, inspectable, and copied into an Oro auth bundle. This
investigation supports that direction and clarifies that `agent-browser` is not
currently a replacement for that layer.
