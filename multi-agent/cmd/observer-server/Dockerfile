FROM golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
  go build -trimpath -ldflags='-s -w' -o /out/observer-server ./cmd/observer-server

FROM debian:bookworm-slim

RUN apt-get update && \
  apt-get install -y --no-install-recommends ca-certificates && \
  rm -rf /var/lib/apt/lists/*

COPY --from=build /out/observer-server /usr/local/bin/observer-server

USER nobody:nogroup
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/observer-server"]
