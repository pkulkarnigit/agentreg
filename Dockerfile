FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/apreg-server ./cmd/apreg-server
RUN CGO_ENABLED=0 go build -o /out/apreg ./cmd/apreg

FROM alpine:3.20
RUN adduser -D -h /home/apreg apreg
COPY --from=build /out/apreg-server /usr/local/bin/apreg-server
COPY --from=build /out/apreg /usr/local/bin/apreg
# The crawler's catalog snapshot (cmd/crawler, internal/web's /catalog
# page) is a checked-in artifact, not runtime-generated — baked into the
# image so it's there without a volume. Refreshing it means re-running
# the crawler and rebuilding, matching how infrequently that data changes.
COPY --from=build /src/catalog /home/apreg/catalog
# Pre-create and chown the data dir before the volume mount: a named
# volume mounted over a directory that already exists in the image
# inherits that directory's ownership on first creation, which is what
# lets the non-root `apreg` user write to it.
RUN mkdir -p /home/apreg/data && chown -R apreg:apreg /home/apreg
USER apreg
WORKDIR /home/apreg
ENV APREG_DATA_DIR=/home/apreg/data
ENV APREG_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["apreg-server"]
