# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /kube-pilot ./cmd/operator

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata bash curl git openssh-client

# Install kubectl
RUN curl -sL "https://dl.k8s.io/release/$(curl -sL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

# Install helm
RUN curl -sL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | VERIFY_CHECKSUM=false bash

# Install gh CLI
RUN curl -sL https://github.com/cli/cli/releases/download/v2.65.0/gh_2.65.0_linux_amd64.tar.gz | tar xz -C /tmp \
    && mv /tmp/gh_2.65.0_linux_amd64/bin/gh /usr/local/bin/gh \
    && rm -rf /tmp/gh_*

# Create non-root user
RUN adduser -D -u 1000 pilot

WORKDIR /app

COPY --from=builder /kube-pilot /app/kube-pilot

RUN mkdir -p /home/pilot/.kube && chown -R pilot:pilot /home/pilot

USER pilot

EXPOSE 8080

ENTRYPOINT ["/app/kube-pilot"]
CMD ["--config", "/etc/kube-pilot/config.yaml"]
