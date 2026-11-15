# Sécurité

Ce document explique les choix de sécurité de l'appli et leurs raisons. Le fonctionnement détaillé
est dans le [README](README.fr.md) (« Points de conception ») et dans le code.
[English version](SECURITY.md).

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
- **Pas d'inscription libre, pas d'e-mail** : un compte se crée par un lien d'invitation à usage
  unique, qu'un admin crée et transmet lui-même. Pas de « mot de passe oublié » par e-mail non plus :
  un admin crée un lien de nouveau mot de passe. Rien à voler par la messagerie, et pas de serveur de
  messagerie à configurer.

### Liens d'invitation et de nouveau mot de passe

- Jeton aléatoire de 256 bits dans l'URL ; **seul son SHA-256 est en base**. Un lien n'est affiché
  qu'une fois, ne sert qu'une fois (consommé dans la transaction qui crée le compte ou change le mot
  de passe) et expire : 7 jours pour une invitation, 24 h pour un nouveau mot de passe ou le premier
  compte.
- L'admin ne voit jamais le mot de passe : la personne le choisit sur la page du lien. Un nouveau mot
  de passe révoque toutes les sessions du compte.
- Le lien du premier compte n'est écrit que dans les journaux, au démarrage, et seulement tant que
  l'instance n'a aucun compte ; il est renouvelé (l'ancien ne sert plus) à chaque démarrage, et
  supprimé dès qu'un premier compte est créé en ligne de commande.
- Les liens créés par un admin ne servent plus dès que ce compte perd le rôle admin ou est supprimé.
- La page du lien pose `Referrer-Policy: same-origin` : le jeton n'est pas envoyé aux autres sites.
  Comme il est dans l'URL, il finit tout de même dans l'historique du navigateur et les journaux
  d'accès du reverse proxy : d'où l'usage unique et l'expiration courte.
  (`no-referrer` ferait envoyer `Origin: null` au formulaire, refusé par la protection CSRF.) Son
  formulaire est protégé comme celui de connexion : `Origin` et jeton CSRF en double soumission.

### Administration

- Les pages d'administration répondent **404** à tout autre compte, comme une liste qu'il ne voit
  pas, et chaque requête est vérifiée côté serveur. Les formulaires sont du HTML simple, avec les
  trois mêmes protections CSRF que l'API.
- **Un admin peut prendre n'importe quel compte** (avec un lien de nouveau mot de passe), et donc
  lire ses listes privées. Ne donner le rôle qu'à des personnes à qui l'on confierait les listes de
  tout le monde.
- L'instance garde toujours au moins un admin : le dernier ne peut pas perdre son rôle, même si deux
  admins se retirent au même moment, et un admin ne peut pas supprimer son propre compte.
- Supprimer un compte supprime ses listes ; ses listes partagées disparaissent chez tout le monde en
  temps réel, et ses sessions et flux temps réel sont coupés aussitôt.
- Changer son mot de passe exige l'actuel, compté comme une tentative de connexion (mêmes blocage par
  IP et ralentissement par compte), déconnecte tous les autres appareils et donne à celui-ci un
  nouveau jeton de session.

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
- Exception, sur demande explicite seulement : `LISTES_INSECURE_HTTP=true` avec une origine
  `http://` hors localhost (essai sur un réseau local). Le navigateur refuserait un cookie `Secure`
  ou `__Host-` reçu en HTTP : les cookies deviennent `listes_session` et `listes_login`, sans
  `Secure`, et circulent en clair. Sans l'option, une origine `http://` hors localhost empêche le
  démarrage, et l'option est refusée avec une origine `https://` : une instance HTTPS ne peut pas
  perdre `Secure` par erreur. Le CSRF (Origin, Sec-Fetch-Site, jeton) ne change pas.
  Les cookies ne sont pas isolés par port : tout autre service HTTP de la même machine
  (`192.168.1.10:8080`) reçoit `listes_session` et peut le poser. Ce mode n'est à utiliser que
  sur une machine dont on fait confiance aux autres services.
- **`SameSite=Lax`, pas `Strict`.** Ouverte depuis l'écran d'accueil (iOS surtout), l'appli démarre
  par une navigation que le navigateur traite comme venue d'un autre site : un cookie `Strict` n'y
  est pas envoyé, et l'appli redemandait la connexion à chaque ouverture. `Lax` n'envoie le cookie,
  depuis un autre site, que sur une navigation GET ; aucune route GET ne modifie de données.
- 90 jours sans visite, 180 jours au plus. Nouvelle session à chaque connexion. Révocation depuis
  la page « Mon compte » (un appareil ou tous les autres), la page d'administration ou la ligne de
  commande (`user reset`, `sessions revoke-all`). Chaque session garde son User-Agent tronqué (200
  caractères) pour qu'on reconnaisse ses appareils ; aucune adresse IP n'est gardée. Le flux temps réel revérifie la
  session toutes les 20 s.

### Requêtes qui modifient : trois protections CSRF

`Origin` obligatoire et égale à `LISTES_ORIGIN`, `Sec-Fetch-Site` égal à `same-origin` s'il est
présent, et jeton CSRF propre à la session. Une seule suffirait en pratique ; les trois ensemble
couvrent les navigateurs anciens et les erreurs de configuration.

### Accès aux listes

- **Une seule règle** (`visibleTo` : propriétaire, liste partagée avec tout le monde, ou membre de
  la liste) et **un seul passage obligé**
  (`accessList` / `accessItem`) pour toutes les routes et le flux temps réel : pas de vérification
  réécrite route par route, donc pas d'oubli possible sur une route.
- Le droit sur un élément vient de sa liste, retrouvée côté serveur ; un identifiant de liste
  envoyé par le client n'est jamais cru.
- Liste privée d'un autre : **404**, pas 403, pour ne pas révéler qu'elle existe.
- Identifiants de **128 bits**, impossibles à deviner.
- Quand le partage change, chaque compte est évalué avant et après : qui gagne l'accès reçoit la
  liste, qui le perd reçoit un simple retrait, sans contenu.
- Tout compte connecté peut lister les noms des autres comptes (pour choisir avec qui partager). Sur
  une instance, chacun sait qui d'autre l'utilise ; ces noms sont aussi les identifiants de connexion.

### Lien public pour cocher seulement

- Un jeton de 256 bits dans l'URL (`/s/<jeton>`) donne accès à **une liste**, pour cocher et
  décocher ses éléments, rien d'autre : aucune route ne permet à un visiteur d'ajouter, modifier,
  déplacer ou supprimer un élément, ni de lire quoi que ce soit sur les comptes, les membres ou le
  partage (la vue publique et ses événements temps réel ne portent que les éléments, le nom et la
  couleur ; pas même le nom du propriétaire, qui est son identifiant de connexion).
- Le jeton est gardé en clair, pour que le propriétaire puisse recopier le lien à tout moment. Il ne
  donne accès qu'à ce que la base contient déjà en clair. Remplacer le lien (ou le couper) invalide
  l'ancien jeton et coupe aussitôt les flux temps réel de ses visiteurs : sous le verrou d'écriture,
  donc aucune modification ne leur parvient après le commit du nouveau jeton, et un flux ouvert
  pendant le changement est revérifié une fois abonné.
- Pas de session, donc pas de jeton CSRF : les écritures exigent l'`Origin` du site (et
  `Sec-Fetch-Site` s'il est présent) et un corps JSON, qui impose une requête préalable CORS depuis
  tout autre site.
- Au plus 120 écritures par minute et par IP ; les flux temps réel anonymes ont leurs propres
  plafonds (64 au total, 16 par lien, 8 par IP), distincts de celui des comptes, qui ne peuvent donc
  jamais en manquer, et qui détient un lien ne peut saturer que celui-là. Une adresse IPv6 compte
  pour tout son /64. Derrière un reverse proxy, ces limites reposent sur `LISTES_TRUSTED_PROXIES` :
  sans lui, tous les visiteurs partagent l'IP du proxy.
- `Referrer-Policy: same-origin` et `X-Robots-Tag: noindex` : le jeton ne sort pas du site et la
  page n'est pas indexée.
- Un lien public ouvert sur un appareil y reste utilisable hors ligne : sa page (les 20 derniers
  liens) et une copie de la liste sont gardées sur l'appareil, même après la déconnexion d'un
  compte, puisque le lien n'en dépend pas. Un lien révoqué est oublié à sa prochaine ouverture
  en ligne.
### Intégrations

- **Jeton d'API** : un par compte, créé dans « Mon compte », affiché une seule fois ; seul son
  SHA-256 est en base. Envoyé en `Authorization: Bearer gsl_…`, il n'est accepté **que** sur
  `/api/v1/*` (ajouter un élément, lister les listes), et ces routes n'acceptent rien d'autre : pas de
  cookie, donc pas de CSRF (un navigateur n'envoie jamais cet en-tête de lui-même). Il permet de voir le
  nom des listes que voit son compte et d'y ajouter des éléments. Le remplacer ou le révoquer prend effet
  aussitôt. Changer le mot de passe, un lien de réinitialisation et « Déconnecter partout » côté admin
  le révoquent aussi, avec les sessions. Au plus 60 appels par minute et par compte.
- **En-tête d'authentification** (`LISTES_AUTH_HEADER`, désactivé par défaut) : le compte nommé est
  connecté sans mot de passe. L'en-tête n'est lu que sur les connexions venant de `127.0.0.1` et de
  `LISTES_TRUSTED_PROXIES`, comme `X-Forwarded-For`, et ne désigne qu'un compte existant. Tout ce qui
  peut joindre l'appli par une adresse de confiance peut donc se connecter sous n'importe quel nom en
  posant l'en-tête : ne l'activer que si **tous** les chemins vers l'appli passent par le proxy
  d'authentification, et que ce proxy écrase toujours l'en-tête. Se déconnecter de l'appli ne sert
  alors à rien (la requête suivante reconnecte) : on se déconnecte au proxy.
- **Import** : 2 Mo au plus, tout ou rien, toujours de nouvelles listes privées ; les limites
  habituelles s'appliquent (listes par compte, éléments par liste, longueur des textes).

### Navigateur

- **CSP stricte, sans script ni style inline**, aucune ressource externe (ni CDN, ni police).
  Avec l'échappement automatique de `html/template`, une injection de script ne s'exécute pas.
- En-têtes posés par l'appli : COOP, CORP, `Permissions-Policy` qui refuse tout sauf le partage
  natif (`web-share=(self)`, pour envoyer le lien public),
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
