FROM golang:1.25.5-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o proxy ./cmd/proxy

FROM alpine:3.18
WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/proxy ./proxy
COPY internal/proxy/sticky.lua ./sticky.lua

EXPOSE 8080

CMD ["./proxy"]