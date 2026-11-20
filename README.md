# giga-simple-lists

Self-hosted checklists, shared in real time. Groceries, today's to-dos, this year's projects: every
list is just a series of checkboxes, always editable. One static Go binary, one SQLite file, a
~16 MB image, under 30 MB of RAM.

[Version française](README.fr.md)

![A grocery list grouped by category (fruit & veg, fresh, pantry): items to check, checked ones at the bottom of their category, tabs for the other lists at the top](docs/screenshots/groceries.png)

<p align="center">
  <img src="docs/screenshots/realtime.gif" width="620" alt="Two phones on the same shared grocery list: items checked or added on one appear on the other in under a second">
</p>

## What it does

- **Lists of checkboxes**, nothing else. Add with Enter, click to check, click the text to edit it.
  Checked items sink to the bottom, greyed out.
- **Real time**: check an item on one phone and it appears on the other in under a second (SSE).
- **Works offline**: the app opens without network on the last lists seen, and changes made offline
  are sent when the connection comes back. Built for the supermarket basement.
- **Several accounts, private or shared lists.** A list is private by default; "Share" makes it
  visible to every account on the instance, or only to the accounts you pick (say, the parents but
  not the kids). They can then rename it and edit its items. Colour, sharing and deletion stay with
  its owner.
- **Check-only public link**: send a link to someone without an account (a friend doing the
  shopping for you). They see the list and check items as they go, live and even offline, and can do
  nothing else: no adding, editing or deleting. Replace or turn off the link at any time.
- **Accounts managed from the app**: invitation links, an admin page, and an "Account" page to change
  one's password and sign out devices. No command line needed.
- **Categories**: typing `# Fruit` creates a heading that groups the items below it (one level).
  It cannot be checked, is struck through once all its items are checked, and moves with them.
  Pasting several lines adds one item per line.
- Drag-and-drop reordering; "Delete checked" clears them in one go, "Uncheck all" resets a recurring
  list (groceries). When nothing is checked, the same button becomes "Check all".
- **Undo** a deletion (item, category, checked items or a whole list): an "Undo" banner appears at
  the bottom, and Ctrl+Z / ⌘Z works too. The last 5 deletions are kept.
- **Duplicate a list** to reuse it as a template ("Packing list"): a private copy, all unchecked.
- **Your data stays yours**: export your lists as JSON from the "Account" page, import them back
  (or into another instance).
- **Integrations**, all optional: a personal API token to add items from Home Assistant or a phone
  shortcut, and sign-in through an authentication proxy (Authelia, Authentik).
- On a phone, the list title opens a bottom sheet with every list and its number of items left.
- Installable web app (iOS, Android), **light and dark mode** following the device setting.
- Interface in **English or French**, chosen once per instance (`LISTES_LANG`).
- Images for **amd64, arm64 and arm/v7** (Raspberry Pi 2 and later).

Who sees what: each account has its private lists, visible only to itself. A list shared with
everyone appears for every account on the instance; a list shared with specific accounts appears
only for them. Those accounts can rename it and edit its items; its colour, sharing and deletion
stay with its owner. A public link only lets people check items.

```mermaid
flowchart TB
  subgraph shared["⇄ Shared"]
    C["Groceries<br/><i>Camille's</i>"]
    W["Weekend in Annecy<br/><i>Sam's</i>"]
  end

  Camille(["👤 Camille"])
  Sam(["👤 Sam"])
  Alex(["👤 Alex"])

  subgraph private["🔒 Private"]
    A["Today<br/><i>Camille's</i>"]
    P["Projects 2027<br/><i>Sam's</i>"]
  end

  shared <==> Camille
  shared <==> Sam
  shared <==> Alex
  Camille <--> A
  Sam <--> P
```

![A list shared by another account, with categories: the same list, editable by both; a category whose items are all checked is struck through](docs/screenshots/shared-list.png)

<p align="center">
  <img src="docs/screenshots/phone.png" width="620" alt="On a phone: the grocery list with categories, then the list sheet open at the bottom of the screen, with the number of items left in each">
</p>

Screenshots taken with made-up data.

## Non-goals

The app does one thing. These are deliberately **not** planned, and pull requests adding them will
be declined:

- due dates, reminders, notifications, recurring tasks;
- priorities, tags, notes, attachments, sub-tasks deeper than one category level;
- a settings page: the few options (language, origin…) live in the environment;
- a native mobile app: the installable web app is the app;
- outgoing connections of any kind (no telemetry, no update check, no external fonts or CDN).

## Quick start

The image is `ghcr.io/fileboss/giga-simple-lists`. Each release `vX.Y.Z` is published as `:X.Y.Z`,
`:X.Y` and `:X`; pin one of them. `:latest` follows `main` and is for development only.

`compose.yaml`:

```yaml
services:
  listes:
    image: ghcr.io/fileboss/giga-simple-lists:2
    restart: unless-stopped
    user: "1000:1000"            # owner of ./data
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    mem_limit: 128m
    pids_limit: 64
    ports:
      - "127.0.0.1:5240:5240"    # only the reverse proxy reaches it
    volumes:
      - ./data:/data
    networks: [listes]
    environment:
      LISTES_ORIGIN: https://lists.example.com   # exact public URL
      LISTES_LANG: en                            # or fr
      # The published port arrives from the network gateway: trust its X-Forwarded-For.
      LISTES_TRUSTED_PROXIES: 172.29.52.1
      GOMEMLIMIT: 96MiB
      TZ: Europe/Paris

networks:
  listes:
    ipam:
      config:
        - subnet: 172.29.52.0/24
          gateway: 172.29.52.1
```

```sh
mkdir -p data && sudo chown 1000:1000 data
docker compose up -d
docker compose logs listes | grep invite   # single-use link to create the first (admin) account
```

Open that link, pick a name and a password: this first account is an admin, and invites everyone
else from the app. The link is renewed at each start until an account exists, and expires after 24 h.

Then put it behind a reverse proxy that serves it over HTTPS (see [Reverse proxy](#reverse-proxy)).
Session cookies are `Secure`.

Container contract: uid 1000, database `/data/listes.db` (a single SQLite file), port 5240,
`GET /healthz` (healthcheck built into the image), `LISTES_ORIGIN` required. Database migrations are
built into the binary and run at startup.

### Trying it on a local network, without HTTPS

Over plain `http://192.168.x.x:5240`, browsers drop `Secure` cookies and sign-in silently fails.
`http://localhost` works out of the box. For any other HTTP address, the app refuses to start and
says why, unless you explicitly opt in:

```yaml
      LISTES_ORIGIN: http://192.168.1.10:5240
      LISTES_INSECURE_HTTP: "true"
```

Cookies then lose their `Secure` flag and `__Host-` prefix: anyone on the network can read them.
Keep this to a trusted LAN. Browsers also disable the service worker over HTTP, so the app does not
open offline and cannot be installed; offline changes are still queued while the page stays open.
The option is refused with an `https://` origin.

## Accounts

There is no open sign-up: people join through invitation links. Everything is done from the app:

- **Account** (your name, at the bottom of the app): change your password, see the devices you are
  signed in on and sign them out. Changing the password signs out every other device.
- **Admin** (admins only): create a single-use invitation link, valid 7 days, optionally for an
  admin; the person picks a name and a password. For each account: a password reset link (valid
  24 h, the admin never sees the password), make or remove admin, sign out everywhere, delete the
  account with its lists. The instance always keeps at least one admin.

Links are shown once and work once; only their hash is stored. On an instance created before
accounts could be managed in the app, the oldest account becomes the admin.

The command line is still there, for scripts or to regain access:

```sh
docker compose exec listes listes user invite [--admin]   # prints a single-use invitation link
docker compose exec listes listes user add <name>         # prints a generated password, once (the first account is an admin)
docker compose exec -T listes listes user add <name> --password-stdin <<<'chosen password (15 characters min.)'
docker compose exec listes listes user reset <name>       # new password, sessions revoked
docker compose exec listes listes user admin <name> [--revoke]
docker compose exec listes listes user list
docker compose exec listes listes sessions revoke-all     # signs out every device
```

## Backup

The image has no shell and no `sqlite3`; the binary makes a consistent copy itself, even while the
server is running:

```sh
docker compose exec listes listes backup                 # /data/listes-backup-<date>.db
docker compose exec listes listes backup /data/today.db  # never overwrites an existing file
```

The copy is a complete SQLite database: to restore, stop the container and replace `listes.db`
with it. `sqlite3 listes.db ".backup copy.db"` from the host works too.

## Integrations

### Adding items from another tool (API token)

On the "Account" page, create a personal API token (shown once, revocable). Then:

```sh
curl -X POST https://lists.example.com/api/v1/items \
  -H 'Authorization: Bearer gsl_…' \
  -H 'Content-Type: application/json' \
  -d '{"list": "Groceries", "text": "Milk"}'
```

- `list`: the list's name (case-insensitive; your own lists first if two share a name) or its id.
  A name shared by several lists of other accounts answers `409`: use the id then.
  `text`: the item, added at the end like the "Add" field. `"category": true` adds a heading.
  Answers `201` with the item; everyone viewing the list sees it live.
- `nonce` (optional, 32 lowercase hex characters): a retry with the same nonce does not add the item
  twice.
- `GET /api/v1/lists` lists the names and ids you can use, with their number of items left.
- At most 60 calls per minute per account. The token can see the names of the lists its account
  sees and add items to them: keep it in a secrets store. Changing the password revokes it.

Home Assistant (`configuration.yaml`), then call `rest_command.add_to_groceries` from an automation
or a voice assistant:

```yaml
rest_command:
  add_to_groceries:
    url: https://lists.example.com/api/v1/items
    method: post
    headers:
      Authorization: !secret lists_token
    content_type: application/json
    payload: '{"list": "Groceries", "text": {{ item | to_json }}}'
```

(`lists_token` in `secrets.yaml` is `Bearer gsl_…`.)

### Signing in through an authentication proxy

If Authelia, Authentik or another proxy already authenticates people and passes their user name in
a header, set `LISTES_AUTH_HEADER=Remote-User`. A request carrying it signs that account in, without
a password. The header is trusted **only** from `127.0.0.1` and `LISTES_TRUSTED_PROXIES`, and only
names an existing account (accounts are still created by invitation). Use it only if **every** path
to the app goes through that proxy and the proxy always sets (overwrites) the header; signing out
then happens at the proxy. To keep the API token working, let only `/api/v1/` through without the
proxy's authentication (not all of `/api/`), and keep the proxy removing the header there too.
See [SECURITY.md](SECURITY.md).

### Export and import

"Account" → "Your data": **Export my lists** downloads a JSON file with the lists you own and their
items. **Import** reads such a file and always creates new, private lists (nothing is merged or
overwritten), so it also moves lists between instances.

## Reverse proxy

The app must be served at the exact address of `LISTES_ORIGIN`. The reverse proxy must:

- **not buffer** the real-time stream (`/api/events`, Server-Sent Events);
- **replace** `X-Forwarded-For` with the client IP (the app reads its last value, to rate-limit
  sign-in attempts per IP);
- add HSTS, `X-Content-Type-Options: nosniff` and `Referrer-Policy`. The app sets the CSP, COOP,
  CORP, `Permissions-Policy`, `X-Frame-Options` and `Cache-Control` itself.

Caddy (automatic HTTPS certificate, and Caddy already replaces `X-Forwarded-For`):

```caddyfile
lists.example.com {
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Content-Type-Options nosniff
        Referrer-Policy strict-origin-when-cross-origin
        -Server
    }
    reverse_proxy 127.0.0.1:5240 {
        flush_interval -1
    }
}
```

nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:5240;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

If the reverse proxy runs on another machine (through a tunnel or a VPN), it must reach port
`127.0.0.1:5240` of the app host through a local relay, and nobody else must be able to reach that
relay: any connection arriving that way is trusted for its `X-Forwarded-For`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `LISTES_ORIGIN` | (required) | Public URL; the only origin accepted for requests that modify data |
| `LISTES_LANG` | `en` | Language of the interface, error messages, command line and logs: `en` or `fr` |
| `LISTES_TRUSTED_PROXIES` | (none) | Comma-separated IPs whose `X-Forwarded-For` is read, in addition to `127.0.0.1`: the Docker network gateway the published port arrives through |
| `LISTES_INSECURE_HTTP` | `false` | Allow an `http://` origin other than localhost (trusted LAN only, see above) |
| `LISTES_AUTH_HEADER` | (none) | Header set by an authentication proxy with the user name (`Remote-User`); trusted only from the proxies above (see Integrations) |
| `LISTES_DB` | `/data/listes.db` | Database path |
| `LISTES_ADDR` | `:5240` | Listen address |
| `TZ` | (UTC) | Time zone (log timestamps) |
| `GOMEMLIMIT` | (none) | Set below the container memory limit (e.g. `96MiB` for 128 MB) |

### Limits

| | |
|---|---|
| Lists per account | 200 |
| Items per list | 2,000 |
| Item text / list name | 500 / 80 characters |
| Open real-time streams | 64 in total, 4 per session |
| Sessions | 90 days without a visit, 180 days at most |
| Database | 50 MB |

## Development

Without Go installed, everything goes through Docker:

```sh
cd app
docker run --rm -v "$PWD:/src" -w /src golang:1.27-alpine sh -c 'go vet ./... && go test ./...'
docker run --rm -v "$PWD:/src" -w /src golang:1.27-alpine go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

Run locally (browsers accept `Secure` cookies on `localhost`):

```sh
mkdir -p data && chmod 777 data
docker compose up -d --build
docker compose exec listes listes user add alice
open http://localhost:5240
```

The repository's `compose.yaml` is for local development only. Every release is a tag (`v1.0.0`,
`v1.0.1`…), published as an image by CI. Dependabot proposes weekly updates of base images, Go
dependencies and GitHub Actions; CI runs `gofmt`, `go vet`, `go test -race`, `govulncheck` (also
nightly) and checks that each platform's image is under 20 MB before publishing it.

Code comments are in French. Translations live in [`app/i18n.go`](app/i18n.go); a test checks that
every language has every key.

## Architecture

Browsers and API clients reach the app only through the reverse proxy (and, optionally, an
authentication proxy). The app is one Go binary in a read-only container, with a single SQLite file
on its volume, and makes no outgoing connection.

![System design: members and guests use a browser or the installable web app, which talks to the reverse proxy over HTTPS and SSE; Home Assistant or a phone shortcut posts to /api/v1 with a token; the reverse proxy optionally asks an authentication proxy, then forwards to the Go app on port 5240, which reads and writes the SQLite file; an admin uses the command line through docker exec. Notes: offline, the service worker serves the cached page and pending changes are resent in order; writes and real-time events share one lock](docs/diagrams/system-design-v2.svg)

### Data model

Every list has one owner and can be shared with every account (`shared`) or with chosen accounts
(`list_members`, "shared with" below); its items follow it. Tokens (session, invitation, API) are
only stored as hashes.

![Data model (Merise MCD): a User opens Sessions, holds at most one ApiToken, owns Lists, is a member of Lists shared with them, last edited Items, creates Invitations and is the target of reset Invitations; a List contains Items; LoginAttempt stands alone, keyed by IP or account. Ids are 128 random bits, times Unix milliseconds, tokens stored as SHA-256 only](docs/diagrams/data-model-v2.svg)

## Design notes

- **Access**: a single rule, `visibleTo` (owner, list shared with everyone, or member of the
  list), and a single gate,
  `accessList` / `accessItem` (`store.go`), for every route. The SSE stream applies `visibleTo` to
  each recipient, on the list's state after the change. Rights on an item come from its list, looked
  up server-side. Someone else's private list: 404.
- **Identifiers**: 128 bits. On creation, they are derived from a random client nonce
  (`sha256(user, nonce)`). The client knows the identifier immediately (optimistic updates, offline
  queue), a resend is idempotent, and another user cannot produce the same identifier.
- **Real time**: SSE. Writes and event publication share one lock, so event order follows commit
  order and visibility is evaluated on the exact state. A list that becomes private again only sends
  others a removal (`{id, reason}`).
- **Offline**: a service worker (`/sw.js`) serves the page and its files network-first, and from
  its cache only if the network does not answer (error, 502-504, or nothing within 4 s): never a
  stale version while online. The API and the SSE stream do not go through it. `app.js` keeps a copy
  of the lists (`localStorage`), shown at startup and then replaced by the server state. Signing out
  clears the cached page and that copy; a redirect to `/login` (expired session) clears the page too.
  Operations are queued in the browser (`localStorage`) and resent in order. Every operation is
  idempotent; "Delete checked" sends the items seen as checked, so a resend does not delete what
  someone else checked in the meantime. 15 s timeout per request, SSE ping every 20 s, watched by
  the client.
- **Security** (choices and reasons: [SECURITY.md](SECURITY.md)): password-only sign-in
  (Argon2id, OWASP parameters), constant response time and a single message; per-IP blocking,
  per-account slowdown only (capped at 1 min, so a third party cannot lock an account out), stored
  in the database; `__Host-session` cookie (only its hash is stored), Origin + Sec-Fetch-Site + CSRF
  token, CSP without inline code. `X-Forwarded-For` is only read from 127.0.0.1 or an address in
  `LISTES_TRUSTED_PROXIES`.
- **Isolation** (a flaw in the app must lead nowhere): distroless without a shell, non-root,
  database capped at 50 MB (507 "Storage full" beyond, reads intact), application log limited to one
  line per minute for the same message. The deployment should add `cap_drop: ALL`,
  `no-new-privileges`, `read_only`, a single volume, `pids_limit`, `nofile`, bounded logs, a port
  published on `127.0.0.1`, and block every outgoing connection: the app needs none.

## License

MIT
