# Changelog

## Unreleased (proposed: v2.3.0)

### Added

- **Duplicate a list**: a private copy for yourself, all unchecked, to reuse a list as a template.
- **Export and import** your lists as JSON from the "Account" page. An import always creates new,
  private lists.
- **Personal API token** and `POST /api/v1/items` to add an item to a list by its name (Home
  Assistant, phone shortcuts); `GET /api/v1/lists` lists the names. See README, Integrations.
  Changing the password, a reset link or an admin "Sign out everywhere" revokes it.
- `LISTES_AUTH_HEADER`: optional sign-in through an authentication proxy header (Authelia,
  Authentik), trusted only from `LISTES_TRUSTED_PROXIES`, for existing accounts.

### Upgrading

- The database is migrated at startup (API tokens). Nothing else changes; no new required variable.

## v2.2.2 (2026-11-15)

### Changed

- Dark mode: lists and categories now use neutral greys, and each list's colour becomes a bright
  accent (dots, checked boxes, colour picker, a line at the top of the card on desktop). The eight
  colours are easy to tell apart again, and the list background matches the page, so no darker band
  shows above or below it on phones. Light mode is unchanged.

## v2.2.1 (2026-11-15)

### Fixed

- Installed app on Android: an empty list no longer needs scrolling to reach the bottom of the page
  (the page height now follows the window's real height: Chrome can keep a larger `100dvh` there).

## v2.2.0 (2026-11-14)

### Added

- **Share a list with specific accounts**, not only with everyone (the parents, not the kids). The
  "Share" button opens a panel: only me, everyone, or specific people.
- **Check-only public link**: anyone with the link can check and uncheck the list's items, without
  an account, live and offline; nothing else. The owner can copy, replace or turn off the link.

### Upgrading

- The database is migrated at startup (list members, public links). Nothing changes for existing
  lists: shared lists stay shared with everyone.

## v2.1.0 (2026-11-14)

### Added

- **Accounts managed from the app**, no more `docker exec`:
  - admin role; the first account is an admin, created through a single-use link written to the
    logs at first start;
  - admin page: invitation links (single use, 7 days, optionally admin), password reset links
    (24 h), make or remove admin, sign out everywhere, delete an account; at least one admin is kept;
  - "Account" page: change password (signs out other devices), signed-in devices with sign-out;
  - command line: `listes user invite [--admin]`, `listes user admin <name> [--revoke]`.

### Upgrading

- The database is migrated at startup: the oldest account becomes the admin, and the logs say which
  one. Sessions are kept.

## v2.0.0 (2026-11-14)

### Breaking

- **The interface is now in English by default.** Set `LISTES_LANG=fr` to keep French. The language
  also applies to API error messages, the command line and logs. Nothing else changes for an existing
  instance: same database (no migration), same cookies and sessions, same environment.

### Added

- `LISTES_LANG` (`en`, `fr`): language of the instance. No language switch in the interface.
- Images for `linux/arm64` and `linux/arm/v7` (Raspberry Pi), in addition to `linux/amd64`, built by
  cross-compilation. Releases are also tagged `:X.Y` and `:X`.
- Dark mode, following the device setting.
- `listes backup [<file>]`: consistent copy of the database while the server runs (the image has no
  `sqlite3`). It opens the database read-only and never migrates or creates it.
- `LISTES_INSECURE_HTTP`: opt-in for an `http://` origin other than localhost, on a trusted LAN.
  Without it, such an origin still stops the app at startup, now with an explanation of the option.
- Signing in from an address other than `LISTES_ORIGIN` now says so, instead of "page expired".
- English README and SECURITY (French versions in `README.fr.md` and `SECURITY.fr.md`), with a
  hardened `compose.yaml` example, a non-goals section, English screenshots and a real-time demo GIF.

### Changed

- The project is named giga-simple-lists, like the repository (docs, logs). The interface keeps the
  short name "Listes" (page titles, home-screen name). Technical names are unchanged so existing
  deployments keep working: binary `listes`, `LISTES_*` variables, `/data/listes.db`, cookies.
