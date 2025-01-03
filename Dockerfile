# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy source code first
COPY . .

# Download and verify modules
RUN go mod download && \
    go mod verify

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/controller cmd/controller/main.go

# Final stage
FROM envoyproxy/envoy:v1.27-latest

# Copy the controller binary
COPY --from=builder /bin/controller /bin/controller

# Copy deployment configurations
COPY deployments/deployment.yaml /etc/envoy-ingress-controller/deployment.yaml

# Create non-root user for security
RUN adduser --system --group controller && \
    chown -R controller:controller /bin/controller /etc/envoy-ingress-controller

# Set necessary capabilities for Envoy
RUN setcap "cap_net_bind_service=+ep" /usr/local/bin/envoy

# Switch to non-root user
USER controller

# Set environment variables
ENV ENVOY_ADMIN_PORT=9901
ENV CONTROLLER_PORT=8080

# Expose ports
EXPOSE 80 443 ${ENVOY_ADMIN_PORT} ${CONTROLLER_PORT}

# Start both Envoy and the controller
CMD ["/bin/controller"]
