# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application (pure Go, no CGO — the Postgres driver needs no C toolchain)
RUN CGO_ENABLED=0 GOOS=linux go build -o recommender

# Use a minimal alpine image for the final stage
FROM alpine:3.24

LABEL org.opencontainers.image.source=https://github.com/icco/recommender
LABEL org.opencontainers.image.description="Daily movie and TV recommendations from a Plex library, enriched with TMDb metadata and chosen by OpenAI; a Go service with Postgres storage."

WORKDIR /app

# Only install runtime dependencies.
RUN apk add --no-cache ca-certificates
RUN adduser -D -u 1000 appuser && \
  mkdir -p /data && \
  chown -R appuser:appuser /data && \
  chown -R appuser:appuser /app

# Copy the binary from builder
COPY --from=builder /app/recommender .

# Set environment variables
ENV POSTER_DIR=/data/posters

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Run the application
CMD ["/app/recommender"]
