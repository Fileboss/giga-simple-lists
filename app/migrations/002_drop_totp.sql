-- Connexion par mot de passe seul : le TOTP est retiré (les listes ne contiennent pas
-- de données sensibles ; la priorité est l'isolation de l'appli, pas la confidentialité).
ALTER TABLE users DROP COLUMN totp_secret;
ALTER TABLE users DROP COLUMN totp_last_step;
