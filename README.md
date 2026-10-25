# giga-simple-lists

Des listes à cocher, partagées en temps réel, à auto-héberger. Courses, choses à faire aujourd'hui,
projets de l'année : chaque liste n'est qu'une suite de cases à cocher, toujours modifiable.

- Plusieurs listes, plusieurs comptes. Une liste est privée par défaut ; la case « Partager » la
  rend visible et modifiable par tous les comptes de l'instance.
- Temps réel : une case cochée sur un téléphone apparaît sur l'autre en moins d'une seconde.
- Hors ligne : les actions faites sans réseau sont renvoyées au retour de la connexion.
- Appli web installable sur l'écran d'accueil (iOS, Android), interface en français.
- Un binaire Go statique et une base SQLite : moins de 30 Mo de RAM, image de 17 Mo.

## Installer

Il faut Docker avec Compose, et un reverse proxy qui sert l'appli en HTTPS (les cookies de session
sont `Secure`).

```sh
git clone https://github.com/Fileboss/giga-simple-lists.git && cd giga-simple-lists
git checkout v1.0.0                       # dernière version taguée
cp .env.example .env && chmod 600 .env    # renseigne LISTES_ORIGIN (URL publique)
install -d -o 1000 -g 1000 data           # propriétaire = LISTES_UID:LISTES_GID
docker compose up -d --build
docker compose ps                         # listes : healthy
```

Le conteneur n'écoute que sur `127.0.0.1:5240`.

### Comptes

Il n'y a pas d'inscription en ligne : les comptes se gèrent en ligne de commande.

```sh
docker compose exec listes listes user add <nom>       # affiche un mot de passe généré, une seule fois
docker compose exec -T listes listes user add <nom> --password-stdin <<<'mot de passe choisi (15 caractères min.)'
docker compose exec listes listes user reset <nom>     # nouveau mot de passe, sessions révoquées
docker compose exec listes listes user list
docker compose exec listes listes sessions revoke-all  # déconnecte tous les appareils
```

### Reverse proxy

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

### Isolation réseau (recommandé)

L'appli n'a besoin d'aucune connexion sortante. `contrib/listes-egress.service` l'interdit au niveau
de l'hôte : si l'appli était compromise, elle ne pourrait joindre ni Internet, ni le réseau local,
ni les autres conteneurs, ni les services de l'hôte.

```sh
sudo install -m 644 contrib/listes-egress.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now listes-egress.service
# Vérification : depuis le réseau de l'appli, tout doit échouer.
docker run --rm --network listes_listes busybox wget -T 3 -qO- http://1.1.1.1 ; echo "code $?"
```

### Sauvegarde

La base est un seul fichier SQLite. Copie-la à chaud avec `sqlite3 .backup`, jamais par simple
copie de fichier, sous l'uid propriétaire pour ne pas créer de fichiers que l'appli ne pourrait plus
ouvrir :

```sh
sudo setpriv --reuid=1000 --regid=1000 --clear-groups \
  sqlite3 data/listes.db ".timeout 30000" ".backup '/chemin/sauvegarde/listes.db'"
```

Pour restaurer : arrêter l'appli, remplacer `data/listes.db` (et supprimer `listes.db-wal` et
`listes.db-shm`), redémarrer.

### Mettre à jour

```sh
git fetch --tags && git checkout vX.Y.Z
docker compose build --pull && docker compose up -d
```

Les migrations de la base sont intégrées au binaire et s'appliquent au démarrage. Fais une
sauvegarde juste avant.

## Configuration

| Variable | Défaut | Rôle |
|---|---|---|
| `LISTES_ORIGIN` | (obligatoire) | URL publique ; seule origine acceptée pour les requêtes qui modifient |
| `LISTES_DATA_LOCATION` | `./data` | dossier de la base sur l'hôte |
| `LISTES_UID`, `LISTES_GID` | `1000` | utilisateur du conteneur, propriétaire du dossier de données |
| `LISTES_PORT` | `5240` | port publié sur `127.0.0.1` |
| `LISTES_TRUSTED_PROXIES` | passerelle du réseau `listes` | IP dont `X-Forwarded-For` est lu, en plus de `127.0.0.1` |

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
LISTES_ORIGIN=http://localhost:5240 docker compose up -d --build
LISTES_ORIGIN=http://localhost:5240 docker compose exec listes listes user add alice
open http://localhost:5240
```

Chaque version est un tag (`v1.0.0`, `v1.0.1`…). Dependabot propose chaque semaine les mises à jour
des images de base, des dépendances Go et des actions GitHub ; la CI lance `gofmt`, `go vet`,
`go test -race`, `govulncheck` (aussi chaque nuit) et vérifie que l'image fait moins de 20 Mo.

## Points de conception

- **Accès** : une seule règle, `visibleTo` (propriétaire, ou liste partagée), et un seul passage
  obligé, `accessList` / `accessItem` (`store.go`), pour toutes les routes et le flux SSE.
  Le droit sur un élément vient de sa liste, retrouvée côté serveur. Liste privée d'un autre : 404.
- **Identifiants** : 128 bits. À la création, ils sont dérivés d'un nonce aléatoire du client
  (`sha256(utilisateur, nonce)`). Le client connaît l'identifiant tout de suite (mises à jour
  optimistes, file hors ligne), un renvoi est idempotent, et un autre utilisateur ne peut pas
  produire le même identifiant.
- **Temps réel** : SSE. Les écritures et la publication des événements partagent un même verrou,
  donc l'ordre des événements suit celui des commits et la visibilité est évaluée sur l'état exact.
  Une liste qui redevient privée n'envoie aux autres qu'un retrait (`{id, reason}`).
- **Hors ligne** : file d'opérations dans le navigateur (`localStorage`), renvoyée dans l'ordre.
  Délai de 15 s par requête, ping SSE toutes les 20 s, surveillé côté client.
- **Sécurité** : connexion par mot de passe seul (Argon2id, paramètres OWASP), temps de réponse
  constant et message unique ; blocage par IP, simple ralentissement par compte (plafonné à 1 min,
  pour qu'un tiers ne puisse pas bloquer un compte), stockés en base ; cookie `__Host-session`
  (seul le hash est en base), Origin + Sec-Fetch-Site + jeton CSRF, CSP sans inline.
  `X-Forwarded-For` n'est lu que depuis 127.0.0.1 ou la passerelle Docker du réseau `listes`.
- **Isolation** (une faille dans l'appli ne doit mener nulle part) : distroless sans shell,
  non-root, `cap_drop: ALL`, `read_only`, un seul volume, `pids_limit`, `nofile`, journaux bornés,
  base plafonnée à 50 Mo (507 « Stockage plein » au-delà, lecture intacte), journal applicatif
  limité à une ligne par minute pour un même message, et aucune connexion sortante
  (`contrib/listes-egress.service`).
