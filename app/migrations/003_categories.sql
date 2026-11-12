-- Catégories : un élément-titre, sans case à cocher, qui regroupe les éléments placés
-- sous lui jusqu'à la catégorie suivante (un seul niveau, par construction).
ALTER TABLE items ADD COLUMN category INTEGER NOT NULL DEFAULT 0 CHECK (category IN (0, 1));
