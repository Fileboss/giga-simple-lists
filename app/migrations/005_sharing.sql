-- Partage fin : une liste peut être partagée avec des comptes précis (list_members), en
-- plus de « tout le monde » (lists.shared), et avoir un lien public où l'on ne peut que
-- cocher (lists.public_token).

CREATE TABLE list_members (
  list_id TEXT NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (list_id, user_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX list_members_user ON list_members(user_id);

-- Jeton du lien public, en clair : le propriétaire doit pouvoir le recopier à tout
-- moment, et il ne donne accès qu'à ce que la base contient déjà en clair (la liste).
ALTER TABLE lists ADD COLUMN public_token TEXT;
CREATE UNIQUE INDEX lists_public_token ON lists(public_token) WHERE public_token IS NOT NULL;
