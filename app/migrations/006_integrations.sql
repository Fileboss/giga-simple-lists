-- Jeton d'API personnel (un par compte) : seul son SHA-256 est stocké. Il permet d'ajouter
-- des éléments depuis un autre outil (Home Assistant, raccourci), sans session ni cookie.
CREATE TABLE api_tokens (
  user_id      TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  token_hash   BLOB NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0
) STRICT;
