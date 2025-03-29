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
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# Final stage
FROM alpine:latest

WORKDIR /app

# Install SQLite
RUN apk add --no-cache sqlite

# Copy the binary from builder
COPY --from=builder /app/main .
COPY --from=builder /app/templates ./templates

# Create directory for SQLite database and set permissions
RUN mkdir -p /app/data && chmod 777 /app/data

# Expose port
EXPOSE 8080

# Set environment variables
ENV PORT=8080

# Run the application
CMD ["./main"] 