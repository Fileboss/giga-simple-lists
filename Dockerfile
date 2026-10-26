# Listes : image publiée par la CI sur ghcr.io/fileboss/giga-simple-lists (voir README).
# Un binaire Go statique (sans CGO : SQLite via modernc.org/sqlite) dans distroless/static :
# ni shell ni gestionnaire de paquets. Le healthcheck est une sous-commande du binaire.
# Contrat : uid 1000, base dans /data/listes.db, port 5240, GET /healthz, LISTES_ORIGIN obligatoire.

FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src
COPY app/go.mod app/go.sum ./
RUN go mod download && go mod verify
COPY app/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/listes .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/listes /usr/local/bin/listes
USER 1000:1000
EXPOSE 5240
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["listes", "healthcheck"]
ENTRYPOINT ["listes"]
CMD ["serve"]
