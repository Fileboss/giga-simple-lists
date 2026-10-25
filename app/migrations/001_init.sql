-- Horodatages en millisecondes Unix. Identifiants : 128 bits aléatoires en hexadécimal.

CREATE TABLE users (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash  TEXT NOT NULL,             -- PHC Argon2id
  totp_secret    TEXT NOT NULL,             -- base32
  totp_last_step INTEGER NOT NULL DEFAULT 0, -- dernier pas TOTP accepté : un code ne sert qu'une fois
  created_at     INTEGER NOT NULL
) STRICT;

-- Seul le SHA-256 du jeton de session est stocké.
CREATE TABLE sessions (
  token_hash   BLOB PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token   TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
) STRICT;
CREATE INDEX sessions_user ON sessions(user_id);

-- Échecs de connexion par clé ("ip:…" ou "user:…"), pour survivre aux redémarrages.
CREATE TABLE login_attempts (
  key             TEXT PRIMARY KEY,
  failures        INTEGER NOT NULL,
  last_failure_at INTEGER NOT NULL,
  locked_until    INTEGER NOT NULL
) STRICT;

CREATE TABLE lists (
  id         TEXT PRIMARY KEY,
  owner_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  color      TEXT NOT NULL,
  shared     INTEGER NOT NULL DEFAULT 0 CHECK (shared IN (0, 1)),
  position   INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX lists_owner ON lists(owner_id);

CREATE TABLE items (
  id         TEXT PRIMARY KEY,
  list_id    TEXT NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  text       TEXT NOT NULL,
  checked    INTEGER NOT NULL DEFAULT 0 CHECK (checked IN (0, 1)),
  position   INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  updated_by TEXT REFERENCES users(id) ON DELETE SET NULL
) STRICT;
CREATE INDEX items_list ON items(list_id, position);
