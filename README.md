# giga-simple-lists

Des listes à cocher, partagées en temps réel, à auto-héberger. Courses, choses à faire aujourd'hui,
projets de l'année : chaque liste n'est qu'une suite de cases à cocher, toujours modifiable.

- Plusieurs listes, plusieurs comptes. Une liste est privée par défaut ; la case « Partager » la
  rend visible par tous les comptes de l'instance. Chacun peut alors la renommer et en modifier les
  éléments ; la couleur, le partage et la suppression restent réservés à son propriétaire.
- Éléments réordonnables par glisser-déposer ; « Retirer les cochés » retire d'un coup les éléments cochés, « Tout décocher » les remet à faire (courses récurrentes).
- Temps réel : une case cochée sur un téléphone apparaît sur l'autre en moins d'une seconde.
- Hors ligne : l'appli s'ouvre sans réseau sur les dernières listes vues, et les actions faites
  sans réseau sont renvoyées au retour de la connexion.
- Appli web installable sur l'écran d'accueil (iOS, Android), interface en français.
- Un binaire Go statique et une base SQLite : moins de 30 Mo de RAM, image d'environ 16 Mo.

## Déploiement

La CI publie l'image `ghcr.io/fileboss/giga-simple-lists` (amd64) : chaque tag `vX.Y.Z` donne
`:vX.Y.Z` et `:X.Y.Z`, les versions à épingler ; `main` donne `:latest` et `:sha-…`, pour le dev
seulement. Ce dépôt ne fournit que l'image : son `compose.yaml` ne sert qu'au dev local.

Contrat du conteneur : uid 1000, base `/data/listes.db` (un seul fichier SQLite, à sauvegarder avec
`sqlite3 .backup`), port 5240, `GET /healthz` (healthcheck intégré à l'image), `LISTES_ORIGIN`
obligatoire. Il faut un reverse proxy qui sert l'appli en HTTPS : les cookies de session sont
`Secure`. Les migrations de la base sont intégrées au binaire et s'appliquent au démarrage.

## Comptes

Il n'y a pas d'inscription en ligne : les comptes se gèrent en ligne de commande.

```sh
docker compose exec listes listes user add <nom>       # affiche un mot de passe généré, une seule fois
docker compose exec -T listes listes user add <nom> --password-stdin <<<'mot de passe choisi (15 caractères min.)'
docker compose exec listes listes user reset <nom>     # nouveau mot de passe, sessions révoquées
docker compose exec listes listes user list
docker compose exec listes listes sessions revoke-all  # déconnecte tous les appareils
```

## Reverse proxy

L'appli doit être servie à l'adresse exacte de `LISTES_ORIGIN`. Le reverse proxy doit :

- **ne pas mettre en tampon** le flux temps réel (`/api/events`, Server-Sent Events) ;
- **remplacer** `X-Forwarded-For` par l'IP du client (l'appli en lit la dernière valeur, pour
  limiter les tentatives de connexion par IP) ;
- ajouter HSTS, `X-Content-Type-Options: nosniff` et `Referrer-Policy`. L'appli pose elle-même la
  CSP, COOP, CORP, `Permissions-Policy`, `X-Frame-Options` et `Cache-Control`.

Caddy (le certificat HTTPS est automatique, et Caddy remplace déjà `X-Forwarded-For`) :

```caddyfile
listes.example.com {
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

nginx :

```nginx
location / {
    proxy_pass http://127.0.0.1:5240;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

Si le reverse proxy tourne sur une autre machine (par un tunnel ou un VPN), il faut qu'il atteigne
le port `127.0.0.1:5240` de l'hôte de l'appli par un relais local, et que personne d'autre ne puisse
joindre ce relais : toute connexion qui arrive par là est crue sur son `X-Forwarded-For`.

## Configuration

| Variable | Défaut | Rôle |
|---|---|---|
| `LISTES_ORIGIN` | (obligatoire) | URL publique ; seule origine acceptée pour les requêtes qui modifient |
| `LISTES_TRUSTED_PROXIES` | (aucune) | IP, séparées par des virgules, dont `X-Forwarded-For` est lu, en plus de `127.0.0.1` : la passerelle du réseau Docker par laquelle arrive le port publié |
| `LISTES_DB` | `/data/listes.db` | chemin de la base |
| `LISTES_ADDR` | `:5240` | adresse d'écoute |
| `TZ` | (UTC) | fuseau horaire (horodatage des journaux) |
| `GOMEMLIMIT` | (aucune) | à régler sous la limite mémoire du conteneur (ex. `96MiB` pour 128 Mo) |

### Limites

| | |
|---|---|
| Listes par compte | 200 |
| Éléments par liste | 2 000 |
| Texte d'un élément / nom de liste | 500 / 80 caractères |
| Flux temps réel ouverts | 64 au total, 4 par session |
| Sessions | 90 jours sans visite, 180 jours au plus |
| Base de données | 50 Mo |

## Développer

Sans Go installé, tout passe par Docker :

```sh
cd app
docker run --rm -v "$PWD:/src" -w /src golang:1.27-alpine sh -c 'go vet ./... && go test ./...'
docker run --rm -v "$PWD:/src" -w /src golang:1.27-alpine go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

Lancer en local (le navigateur accepte les cookies `Secure` sur `localhost`) :

```sh
mkdir -p data && chmod 777 data
docker compose up -d --build
docker compose exec listes listes user add alice
open http://localhost:5240
```

Chaque version est un tag (`v1.0.0`, `v1.0.1`…), publié comme image par la CI. Dependabot propose
chaque semaine les mises à jour des images de base, des dépendances Go et des actions GitHub ; la CI
lance `gofmt`, `go vet`, `go test -race`, `govulncheck` (aussi chaque nuit) et vérifie que l'image
fait moins de 20 Mo avant de la publier.

## Points de conception

- **Accès** : une seule règle, `visibleTo` (propriétaire, ou liste partagée), et un seul passage
  obligé, `accessList` / `accessItem` (`store.go`), pour toutes les routes. Le flux SSE applique
  `visibleTo` à chaque destinataire, sur l'état de la liste après la modification.
  Le droit sur un élément vient de sa liste, retrouvée côté serveur. Liste privée d'un autre : 404.
- **Identifiants** : 128 bits. À la création, ils sont dérivés d'un nonce aléatoire du client
  (`sha256(utilisateur, nonce)`). Le client connaît l'identifiant tout de suite (mises à jour
  optimistes, file hors ligne), un renvoi est idempotent, et un autre utilisateur ne peut pas
  produire le même identifiant.
- **Temps réel** : SSE. Les écritures et la publication des événements partagent un même verrou,
  donc l'ordre des événements suit celui des commits et la visibilité est évaluée sur l'état exact.
  Une liste qui redevient privée n'envoie aux autres qu'un retrait (`{id, reason}`).
- **Hors ligne** : un service worker (`/sw.js`) sert la page et ses fichiers depuis le réseau
  d'abord, et depuis son cache seulement si le réseau ne répond pas (erreur, 502-504, ou rien
  sous 4 s) : jamais de version périmée en ligne. L'API et le flux SSE ne passent pas par lui.
  `app.js` garde une copie des listes (`localStorage`), affichée à l'ouverture puis remplacée par
  l'état du serveur. La déconnexion efface la page en cache et cette copie ; une redirection vers
  `/login` (session expirée) efface aussi la page.
  File d'opérations dans le navigateur (`localStorage`), renvoyée dans l'ordre.
  Chaque opération est idempotente ; « Vider les cochés » envoie les éléments vus cochés, pour ne
  pas supprimer au renvoi ce qu'un autre a coché entre-temps.
  Délai de 15 s par requête, ping SSE toutes les 20 s, surveillé côté client.
- **Sécurité** (choix et raisons : [SECURITY.md](SECURITY.md)) : connexion par mot de passe seul (Argon2id, paramètres OWASP), temps de réponse
  constant et message unique ; blocage par IP, simple ralentissement par compte (plafonné à 1 min,
  pour qu'un tiers ne puisse pas bloquer un compte), stockés en base ; cookie `__Host-session`
  (seul le hash est en base), Origin + Sec-Fetch-Site + jeton CSRF, CSP sans inline.
  `X-Forwarded-For` n'est lu que depuis 127.0.0.1 ou la passerelle Docker du réseau `listes`.
- **Isolation** (une faille dans l'appli ne doit mener nulle part) : distroless sans shell,
  non-root, base plafonnée à 50 Mo (507 « Stockage plein » au-delà, lecture intacte), journal
  applicatif limité à une ligne par minute pour un même message. Le déploiement doit ajouter
  `cap_drop: ALL`, `read_only`, un seul volume, `pids_limit`, `nofile`, des journaux bornés et
  bloquer toute connexion sortante : l'appli n'en a besoin d'aucune.
