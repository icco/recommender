# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

RUN apk add --no-cache gcc musl-dev git sqlite

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -o recommender

# Use a minimal alpine image for the final stage
FROM alpine:3.21

WORKDIR /app

# Only install runtime dependencies.
# gcc, musl-dev, and git are compile-time tools — they are NOT needed
# to run an already-compiled binary and should not ship in production.
RUN apk add --no-cache ca-certificates sqlite-libs
RUN adduser -D -u 1000 appuser && \
  mkdir -p /data && \
  chown -R appuser:appuser /data && \
  chown -R appuser:appuser /app && \
  touch /data/recommender.db && \
  chown appuser:appuser /data/recommender.db && \
  chmod 777 /data/recommender.db

# Copy the binary from builder
COPY --from=builder /app/recommender .

# Set environment variables
ENV DB_PATH=/data/recommender.db

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Run the application
CMD ["/app/recommender"]
