# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Dependencies first (better layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /driftd ./cmd/driftd

# Runtime stage
FROM alpine:3.21

ARG TARGETARCH

# Install dependencies
RUN apk add --no-cache \
    git \
    openssh-client \
    ca-certificates \
    curl \
    unzip \
    bash

# Install tfswitch
RUN curl -fsSL https://raw.githubusercontent.com/warrensbox/terraform-switcher/master/install.sh | bash

# Install tgswitch
RUN curl -fsSL https://raw.githubusercontent.com/warrensbox/tgswitch/master/install.sh | bash

# Create non-root user
RUN addgroup -g 1000 driftd && \
    adduser -u 1000 -G driftd -s /bin/sh -D driftd

# Create cache and data directories
RUN mkdir -p \
    /cache/terraform/plugins \
    /cache/terraform/versions \
    /cache/terragrunt/download \
    /cache/terragrunt/versions \
    /data && \
    chown -R driftd:driftd /cache /data

# Copy binary
COPY --from=builder /driftd /usr/local/bin/driftd

# Environment variables for caching
ENV TF_PLUGIN_CACHE_DIR=/cache/terraform/plugins \
    TFSWITCH_HOME=/cache/terraform/versions \
    TGSWITCH_HOME=/cache/terragrunt/versions \
    TERRAGRUNT_DOWNLOAD=/cache/terragrunt/download \
    # tfswitch/tgswitch will install binaries here
    PATH="/cache/terraform/versions:/cache/terragrunt/versions:${PATH}"

USER driftd
WORKDIR /home/driftd

VOLUME ["/data", "/cache"]
EXPOSE 8080

ENTRYPOINT ["driftd"]
CMD ["serve", "-config", "/etc/driftd/config.yaml"]
