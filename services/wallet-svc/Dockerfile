FROM golang:1.25-alpine AS builder

WORKDIR /workspace
COPY . .

WORKDIR /workspace/services/wallet-svc

RUN go mod download
RUN go build -o wallet-svc ./cmd


FROM alpine:latest

RUN apk --no-cache add ca-certificates sqlite

WORKDIR /

# place binary at root so /wallet-svc works
COPY --from=builder /workspace/services/wallet-svc/wallet-svc /wallet-svc

COPY services/wallet-svc/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

RUN mkdir -p /data

ENTRYPOINT ["/entrypoint.sh"]