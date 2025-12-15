FROM golang:1.25.5-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o backend ./cmd/backend

FROM alpine:3.18
WORKDIR /app
COPY --from=builder /app/backend ./backend

EXPOSE 5678

CMD ["./backend"]