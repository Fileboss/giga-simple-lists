# Listes : image publiée par la CI sur ghcr.io/fileboss/giga-simple-lists (voir README),
# pour linux/amd64, linux/arm64 et linux/arm/v7 (Raspberry Pi).
# Un binaire Go statique (sans CGO : SQLite via modernc.org/sqlite) dans distroless/static :
# ni shell ni gestionnaire de paquets. Le healthcheck est une sous-commande du binaire.
# Compilation croisée : l'étape de build tourne sur la plateforme de la machine
# (BUILDPLATFORM) et produit le binaire de la cible, sans émulation.
# Contrat : uid 1000, base dans /data/listes.db, port 5240, GET /healthz, LISTES_ORIGIN obligatoire.

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /src
COPY app/go.mod app/go.sum ./
RUN go mod download && go mod verify
COPY app/ ./
ARG TARGETOS TARGETARCH TARGETVARIANT
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags='-s -w' -o /out/listes .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/listes /usr/local/bin/listes
USER 1000:1000
EXPOSE 5240
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["listes", "healthcheck"]
ENTRYPOINT ["listes"]
CMD ["serve"]
