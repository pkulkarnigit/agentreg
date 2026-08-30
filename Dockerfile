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
USER apreg
WORKDIR /home/apreg
ENV APREG_DATA_DIR=/home/apreg/data
ENV APREG_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["apreg-server"]
