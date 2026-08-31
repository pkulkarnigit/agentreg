FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/krate-server ./cmd/krate-server
RUN CGO_ENABLED=0 go build -o /out/krate ./cmd/krate

FROM alpine:3.20
RUN adduser -D -h /home/krate krate
COPY --from=build /out/krate-server /usr/local/bin/krate-server
COPY --from=build /out/krate /usr/local/bin/krate
# The crawler's catalog snapshot (cmd/crawler, internal/web's /catalog
# page) is a checked-in artifact, not runtime-generated — baked into the
# image so it's there without a volume. Refreshing it means re-running
# the crawler and rebuilding, matching how infrequently that data changes.
COPY --from=build /src/catalog /home/krate/catalog
# Pre-create and chown the data dir before the volume mount: a named
# volume mounted over a directory that already exists in the image
# inherits that directory's ownership on first creation, which is what
# lets the non-root `krate` user write to it.
RUN mkdir -p /home/krate/data && chown -R krate:krate /home/krate
USER krate
WORKDIR /home/krate
ENV KRATE_DATA_DIR=/home/krate/data
ENV KRATE_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["krate-server"]
