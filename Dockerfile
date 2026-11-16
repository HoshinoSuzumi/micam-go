# syntax=docker/dockerfile:1

# ---- build -----------------------------------------------------------------
FROM golang:1.23-alpine AS build

WORKDIR /src

# Cache the module download separately from the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/micam ./cmd/micam

# ---- runtime ---------------------------------------------------------------
FROM alpine:3.20

# ca-certificates is required for the HTTPS calls to the Mi cloud.
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 micam

# The session file is written here, so the directory must belong to the user that
# runs the process. Without this a bind-mounted ./data stays root-owned and the
# first start fails with "cannot write the credentials".
RUN mkdir -p /data && chown -R micam:micam /data

COPY --from=build /out/micam /usr/local/bin/micam

# HOME points at the volume, so the credentials land on persistent storage.
# (Unlike the JVM, Go honours $HOME.)
ENV HOME=/data \
    MICAM_BIND=0.0.0.0 \
    MICAM_PORT=8554

USER micam
VOLUME ["/data"]
EXPOSE 8554/tcp

# Note: the stream carries no authentication. Publish the port only on a trusted
# network, or put a reverse proxy with auth in front of it.
ENTRYPOINT ["micam"]
