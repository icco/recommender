# Build stage
FROM golang:1.24.1-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache gcc musl-dev

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o main .

# Final stage
FROM alpine:3.19

WORKDIR /app

# Install SQLite and create non-root user
RUN apk add --no-cache sqlite && \
  adduser -D -u 1000 appuser && \
  mkdir -p /app/data && \
  chown -R appuser:appuser /app/data

# Copy the binary and templates from builder
COPY --from=builder /app/main .
COPY --from=builder /app/templates ./templates

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Set environment variables
ENV PORT=8080

# Run the application
CMD ["./main"] 
