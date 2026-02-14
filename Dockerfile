# Build stage
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Dependencies first (better layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /driftd ./cmd/driftd

# Tools stage (keep curl/bash out of the runtime image)
FROM --platform=$BUILDPLATFORM alpine:3.21 AS tools

ARG TARGETARCH
ARG TFSWITCH_VERSION=v1.13.0
ARG TFSWITCH_SHA256_AMD64=2282f433b1a2a7569ae25d1f0867db225b180ee14814d38b54c2c0492b5ddc58
ARG TFSWITCH_SHA256_ARM64=e69452e0022f78546fa7cb33a5a6bfa339154c1f75970a8ed427ec9fd7402384
ARG TGSWITCH_VERSION=0.6.0
ARG TGSWITCH_SHA256_AMD64=d1513d77b64645b864b04431dc093c651f7a6bb97ef24037a7d75e90dea1601b
ARG TGSWITCH_SHA256_ARM64=808691afcbee1e667f1969c0cc3f220461d57ecfb074d3e0b61d4367dda08d66

RUN apk add --no-cache \
    ca-certificates \
    curl \
    bash

# Install pinned tfswitch/tgswitch with checksum verification
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) tfswitch_sha="${TFSWITCH_SHA256_AMD64}"; tgswitch_sha="${TGSWITCH_SHA256_AMD64}";; \
      arm64) tfswitch_sha="${TFSWITCH_SHA256_ARM64}"; tgswitch_sha="${TGSWITCH_SHA256_ARM64}";; \
      *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1;; \
    esac; \
    tfswitch_file="terraform-switcher_${TFSWITCH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    tgswitch_file="tgswitch_${TGSWITCH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    curl -fsSL -o "/tmp/${tfswitch_file}" "https://github.com/warrensbox/terraform-switcher/releases/download/${TFSWITCH_VERSION}/${tfswitch_file}"; \
    echo "${tfswitch_sha}  /tmp/${tfswitch_file}" | sha256sum -c -; \
    tar -xzf "/tmp/${tfswitch_file}" -C /usr/local/bin tfswitch; \
    chmod 0755 /usr/local/bin/tfswitch; \
    curl -fsSL -o "/tmp/${tgswitch_file}" "https://github.com/warrensbox/tgswitch/releases/download/${TGSWITCH_VERSION}/${tgswitch_file}"; \
    echo "${tgswitch_sha}  /tmp/${tgswitch_file}" | sha256sum -c -; \
    tar -xzf "/tmp/${tgswitch_file}" -C /usr/local/bin tgswitch; \
    chmod 0755 /usr/local/bin/tgswitch; \
    rm -f "/tmp/${tfswitch_file}" "/tmp/${tgswitch_file}"

# Runtime stage
FROM alpine:3.21

# Install dependencies
RUN apk add --no-cache \
    git \
    openssh-client \
    ca-certificates \
    unzip \
    bash

# Create non-root user
RUN addgroup -S -g 1000 driftd && \
    adduser -S -D -H -u 1000 -G driftd -s /sbin/nologin driftd

# Create cache and data directories
RUN mkdir -p \
    /cache/terraform/plugins \
    /cache/terraform/versions \
    /cache/terragrunt/download \
    /cache/terragrunt/versions \
    /data \
    /home/driftd && \
    chown -R driftd:driftd /cache /data /home/driftd

# Copy binary
COPY --from=builder /driftd /usr/local/bin/driftd
COPY --from=tools /usr/local/bin/tfswitch /usr/local/bin/tfswitch
COPY --from=tools /usr/local/bin/tgswitch /usr/local/bin/tgswitch

# Environment variables for caching
ENV TF_PLUGIN_CACHE_DIR=/cache/terraform/plugins \
    TFSWITCH_HOME=/cache/terraform/versions \
    TGSWITCH_HOME=/cache/terragrunt/versions \
    TERRAGRUNT_DOWNLOAD=/cache/terragrunt/download \
    HOME=/home/driftd \
    # tfswitch/tgswitch will install binaries here
    PATH="/cache/terraform/versions:/cache/terragrunt/versions:${PATH}"

USER driftd
WORKDIR /home/driftd

VOLUME ["/data", "/cache"]
EXPOSE 8080

ENTRYPOINT ["driftd"]
CMD ["serve", "-config", "/etc/driftd/config.yaml"]
