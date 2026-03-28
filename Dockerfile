# ── Build stage ──
FROM golang:1.25-alpine AS builder

# Install C compiler for CGO (required by go-sqlite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO must be enabled for sqlite3
ENV CGO_ENABLED=1
RUN go build -o /bin/server ./cmd/server

# ── Run stage ──
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy the compiled binary
COPY --from=builder /bin/server ./server

# Copy assets (SQLite seed data, CSV files, etc.)
COPY --from=builder /src/assets ./assets

EXPOSE 8000

CMD ["./server"]
