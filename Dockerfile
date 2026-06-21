# Stage 1: Build the Go binary
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy dependency files first for caching benefits
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code (including your updated main.go)
COPY . .

# Build the binary under the new project name
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o goodtop-exporter .

# Stage 2: Minimal runtime image
FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /

# Copy the updated binary from the builder stage
COPY --from=builder /app/goodtop-exporter /usr/local/bin/goodtop-exporter

# Expose the default port used by your command flags
EXPOSE 8080

# Set the binary as the entrypoint
ENTRYPOINT ["/usr/local/bin/goodtop-exporter"]
CMD ["--config.file=/config.yaml"]
