-- Comptes gérés depuis l'appli : rôle admin, liens d'invitation et de réinitialisation,
-- appareil de chaque session.

ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1));
-- Instance existante : le plus ancien compte devient admin (un seul, à compléter depuis la
-- page d'administration).
UPDATE users SET is_admin = 1 WHERE id = (SELECT id FROM users ORDER BY created_at, id LIMIT 1);

-- User-Agent à la connexion, tronqué : seulement pour reconnaître ses appareils.
ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';

-- Liens à usage unique. Seul le SHA-256 du jeton est stocké.
--   invite : crée un compte (admin si is_admin) ; created_by NULL pour le premier compte et
--            la ligne de commande ;
--   reset  : choisit un nouveau mot de passe pour user_id.
CREATE TABLE invitations (
  id         TEXT PRIMARY KEY,
  token_hash BLOB NOT NULL UNIQUE,
  kind       TEXT NOT NULL CHECK (kind IN ('invite', 'reset')),
  is_admin   INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  user_id    TEXT REFERENCES users(id) ON DELETE CASCADE,
  created_by TEXT REFERENCES users(id) ON DELETE CASCADE, -- liens d'un compte supprimé : caducs
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  CHECK ((kind = 'reset') = (user_id IS NOT NULL))
) STRICT;
