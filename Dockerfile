# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o recommender

# Use a minimal alpine image for the final stage
FROM alpine:latest

WORKDIR /app

# Install SQLite and create non-root user
RUN apk add --no-cache sqlite && \
  adduser -D -u 1000 appuser && \
  mkdir -p /data && \
  chown -R appuser:appuser /data

# Copy the binary and templates from builder
COPY --from=builder /app/recommender .
COPY --from=builder /app/templates ./templates

# Set environment variables
ENV DB_PATH=/data/recommender.db

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Run the application
CMD ["./recommender"] 
