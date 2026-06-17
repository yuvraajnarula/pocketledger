FROM golang:1.25-alpine AS builder

WORKDIR /workspace

COPY . .

WORKDIR /workspace/services/ledger-svc

RUN go mod download
RUN go build -o ledger-svc ./cmd


FROM alpine:latest

RUN apk --no-cache add ca-certificates sqlite

WORKDIR /

COPY --from=builder /workspace/services/ledger-svc/ledger-svc /ledger-svc
COPY services/ledger-svc/internal/policy.yml /etc/ledger/policy.yaml

COPY services/ledger-svc/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

RUN mkdir -p /data

ENTRYPOINT ["/entrypoint.sh"]