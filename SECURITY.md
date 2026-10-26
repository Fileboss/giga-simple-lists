# Sécurité

Ce document explique les choix de sécurité de l'appli et leurs raisons. Le fonctionnement détaillé
est dans le [README](README.md) (« Points de conception ») et dans le code.

## Signaler une faille

Pas d'issue publique : utilise *Security → Report a vulnerability* sur la page GitHub du dépôt
(signalement privé). Indique la version (tag ou image) et les étapes pour reproduire.

## Modèle de menace

L'appli est faite pour être **exposée sur Internet** : des robots essaieront de se connecter dès le
premier jour. Elle contient des listes de courses et de choses à faire, **pas de données
sensibles**.

D'où la priorité : **une faille dans l'appli ne doit mener nulle part.** Ni à l'hôte, ni au réseau
local, ni aux autres services qui tournent à côté. La confidentialité des listes passe après.

## Décisions

### Connexion par mot de passe seul

- **Pas de second facteur** (TOTP, passkeys). Une première version demandait un code TOTP à chaque
  connexion : trop contraignant pour des listes de courses, pour un gain faible. Le mot de passe
  généré par `listes user add` fait 24 caractères (environ 120 bits), impossible à deviner, et les
  tentatives sont limitées.
- **Argon2id**, paramètres de l'OWASP (m = 19 Mio, t = 2, p = 1), au plus 2 calculs simultanés pour
  tenir dans la limite mémoire du conteneur.
- **Même message et même durée** (800 ms au moins) pour toute connexion refusée : compte
  inexistant, mot de passe faux, IP bloquée. On ne peut pas deviner quels comptes existent.
- **Pas d'inscription en ligne, pas de « mot de passe oublié » par e-mail, pas d'administration
  web** : les comptes se gèrent uniquement en ligne de commande. Moins de surface d'attaque, et rien
  à voler par la messagerie.

### Limitation des tentatives

- **Par IP : blocage.** Au-delà de 10 échecs en 24 h, l'IP est bloquée 30 s, puis 1 min, 2 min…
  jusqu'à 1 h.
- **Par compte : simple ralentissement**, plafonné à 1 min (au plus 32 tentatives ralenties en
  attente). Un vrai blocage par compte permettrait à n'importe qui connaissant un nom d'empêcher
  son propriétaire de se connecter.
- Les compteurs sont **stockés en base** : un redémarrage ne les remet pas à zéro.

### Sessions

- Jeton aléatoire de 256 bits dans le cookie ; **seul son SHA-256 est en base** : une copie de la
  base ne donne accès à aucune session.
- Cookie `__Host-session` : `Secure`, `HttpOnly`, `Path=/`, sans domaine.
- **`SameSite=Lax`, pas `Strict`.** Ouverte depuis l'écran d'accueil (iOS surtout), l'appli démarre
  par une navigation que le navigateur traite comme venue d'un autre site : un cookie `Strict` n'y
  est pas envoyé, et l'appli redemandait la connexion à chaque ouverture. `Lax` n'envoie le cookie,
  depuis un autre site, que sur une navigation GET ; aucune route GET ne modifie de données.
- 90 jours sans visite, 180 jours au plus. Nouvelle session à chaque connexion. Révocation depuis
  la ligne de commande (`user reset`, `sessions revoke-all`). Le flux temps réel revérifie la
  session toutes les 20 s.

### Requêtes qui modifient : trois protections CSRF

`Origin` obligatoire et égale à `LISTES_ORIGIN`, `Sec-Fetch-Site` égal à `same-origin` s'il est
présent, et jeton CSRF propre à la session. Une seule suffirait en pratique ; les trois ensemble
couvrent les navigateurs anciens et les erreurs de configuration.

### Accès aux listes

- **Une seule règle** (`visibleTo` : propriétaire, ou liste partagée) et **un seul passage obligé**
  (`accessList` / `accessItem`) pour toutes les routes et le flux temps réel : pas de vérification
  réécrite route par route, donc pas d'oubli possible sur une route.
- Le droit sur un élément vient de sa liste, retrouvée côté serveur ; un identifiant de liste
  envoyé par le client n'est jamais cru.
- Liste privée d'un autre : **404**, pas 403, pour ne pas révéler qu'elle existe.
- Identifiants de **128 bits**, impossibles à deviner.

### Navigateur

- **CSP stricte, sans script ni style inline**, aucune ressource externe (ni CDN, ni police).
  Avec l'échappement automatique de `html/template`, une injection de script ne s'exécute pas.
- En-têtes posés par l'appli : COOP, CORP, `Permissions-Policy` qui refuse tout,
  `X-Frame-Options: DENY`, `Cache-Control: no-store` sur les pages connectées. HSTS, `nosniff` et
  `Referrer-Policy` sont à ajouter au reverse proxy (README).
- **Hors ligne** : le service worker passe toujours d'abord par le réseau, et n'utilise son cache
  que si le réseau ne répond pas. La copie locale des listes est propre à chaque compte et
  effacée à la déconnexion. Elle reste lisible sur l'appareil tant qu'on est connecté : protéger
  l'appareil lui-même (code de verrouillage) reste nécessaire.

### IP du client

`X-Forwarded-For` n'est lu que si la connexion vient de `127.0.0.1` ou d'une adresse de
`LISTES_TRUSTED_PROXIES`, et seulement sa **dernière** valeur, celle du reverse proxy. Sinon, un
visiteur pourrait se donner l'IP de son choix et contourner le blocage par IP.

### Limites contre la saturation

Une faille ou un compte volé ne doit pas pouvoir remplir le disque ou la mémoire de l'hôte :

- base plafonnée à **50 Mo** (au-delà : erreur 507, lecture toujours possible) ;
- 200 listes par compte, 2 000 éléments par liste, 500 caractères par élément, 80 par nom de liste,
  requêtes de 16 Kio au plus ;
- 64 flux temps réel au total, 4 par session ;
- un même message d'erreur n'est journalisé qu'une fois par minute ;
- délais explicites sur toutes les étapes HTTP.

### Conteneur et déploiement

- Image **distroless** de 16 Mo : pas de shell, pas de gestionnaire de paquets, rien à exploiter
  dans l'image une fois dedans. Le healthcheck est une sous-commande du binaire.
- Tourne en **uid 1000**, jamais en root.
- Le déploiement doit ajouter : `read_only`, `cap_drop: ALL`, `no-new-privileges`, un seul volume
  (`/data`), `pids_limit`, `ulimits nofile`, des journaux bornés, un port publié sur `127.0.0.1`.
- **Aucune connexion sortante** : l'appli n'en a besoin d'aucune, il faut toutes les interdire au
  niveau de l'hôte. Attention, une règle iptables dans la chaîne `DOCKER-USER` ne suffit pas
  toujours : un VPN comme Tailscale place ses propres chaînes avant elle et laisse passer le trafic
  vers son réseau. Une **table nftables à part** (hook `prerouting`, nouvelles connexions entrant
  par l'interface du réseau Docker) ne peut être contournée par aucune autre table. Teste-la avec
  une vraie destination de chaque réseau (Internet, LAN, VPN, hôte).

### Chaîne d'approvisionnement

- **Deux dépendances directes** seulement : `golang.org/x/crypto` (Argon2id) et `modernc.org/sqlite`
  (SQLite en Go pur, sans CGO). Pas de npm, pas d'étape de build front.
- Images de base épinglées par **digest**, actions GitHub épinglées par **SHA**.
- La CI lance `govulncheck` à chaque push et **chaque nuit**. Dependabot propose chaque semaine les
  mises à jour des images de base, des dépendances Go et des actions.
- L'image n'est publiée que depuis `main` et les tags `vX.Y.Z`, jamais depuis une pull request.
  Déployer une version précise (`:X.Y.Z`), jamais `:latest`.

## Ce que l'appli ne protège pas

- **Les listes sont en clair dans la base.** Sans chiffrement de bout en bout, l'hôte et ses
  sauvegardes les voient. N'y mets ni mots de passe, ni données sensibles.
- **Pas de second facteur** : un mot de passe volé (réutilisé ailleurs, hameçonnage) donne accès au
  compte jusqu'à `listes user reset`. D'où le mot de passe généré, à ranger dans un gestionnaire.
- **Le reverse proxy voit tout en clair** : c'est lui qui termine le HTTPS.
