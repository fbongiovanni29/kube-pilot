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

RUN apk add --no-cache ca-certificates tzdata bash curl git openssh-client python3 jq yq ripgrep unzip

# Install kubectl
RUN curl -sL "https://dl.k8s.io/release/$(curl -sL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

# Install helm
RUN curl -sL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | VERIFY_CHECKSUM=false bash

# Install gh CLI
RUN curl -sL https://github.com/cli/cli/releases/download/v2.65.0/gh_2.65.0_linux_amd64.tar.gz | tar xz -C /tmp \
    && mv /tmp/gh_2.65.0_linux_amd64/bin/gh /usr/local/bin/gh \
    && rm -rf /tmp/gh_*

# Install ArgoCD CLI
RUN curl -sL https://github.com/argoproj/argo-cd/releases/latest/download/argocd-linux-amd64 -o /usr/local/bin/argocd \
    && chmod +x /usr/local/bin/argocd

# Install Tekton CLI (tkn)
RUN curl -sL https://github.com/tektoncd/cli/releases/download/v0.39.0/tkn_0.39.0_Linux_x86_64.tar.gz | tar xz -C /tmp \
    && mv /tmp/tkn /usr/local/bin/tkn \
    && rm -rf /tmp/LICENSE /tmp/README.md

# Install logcli (Loki CLI)
RUN curl -sL https://github.com/grafana/loki/releases/download/v3.4.2/logcli-linux-amd64.zip -o /tmp/logcli.zip \
    && unzip -o /tmp/logcli.zip -d /tmp \
    && mv /tmp/logcli-linux-amd64 /usr/local/bin/logcli \
    && chmod +x /usr/local/bin/logcli \
    && rm /tmp/logcli.zip

# Install amtool (Alertmanager CLI)
RUN curl -sL https://github.com/prometheus/alertmanager/releases/download/v0.28.1/alertmanager-0.28.1.linux-amd64.tar.gz | tar xz -C /tmp \
    && mv /tmp/alertmanager-0.28.1.linux-amd64/amtool /usr/local/bin/amtool \
    && chmod +x /usr/local/bin/amtool \
    && rm -rf /tmp/alertmanager-*

# Create non-root user
RUN adduser -D -u 1000 pilot

WORKDIR /app

COPY --from=builder /kube-pilot /app/kube-pilot

RUN mkdir -p /home/pilot/.kube && chown -R pilot:pilot /home/pilot

USER pilot

EXPOSE 8080

ENTRYPOINT ["/app/kube-pilot"]
CMD ["--config", "/etc/kube-pilot/config.yaml"]
