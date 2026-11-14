# Changelog

## Unreleased (proposed: v2.0.0)

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
  `sqlite3`).
- `LISTES_INSECURE_HTTP`: opt-in for an `http://` origin other than localhost, on a trusted LAN.
  Without it, such an origin now stops the app at startup with an explanation instead of a sign-in
  that silently fails.
- Signing in from an address other than `LISTES_ORIGIN` now says so, instead of "page expired".
- English README and SECURITY (French versions in `README.fr.md` and `SECURITY.fr.md`), with a
  hardened `compose.yaml` example, a non-goals section, English screenshots and a real-time demo GIF.

### Changed

- The app is named giga-simple-lists everywhere it shows a name (page titles, home-screen name,
  logs), like the repository. Technical names are unchanged so existing deployments keep working:
  binary `listes`, `LISTES_*` variables, `/data/listes.db`, cookies.
