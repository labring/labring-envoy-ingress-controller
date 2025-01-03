# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install git for go mod download
RUN apk add --no-cache git

# Copy the source code
COPY . .

# Download dependencies and verify modules
RUN go mod download && \
    go mod verify

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/controller cmd/controller/main.go

# Final stage
FROM envoyproxy/envoy:v1.27-latest

# Copy the controller binary
COPY --from=builder /bin/controller /bin/controller

# Copy deployment configuration
COPY deployments/deployment.yaml /etc/envoy-ingress-controller/deployment.yaml

# Create non-root user
RUN adduser --system --group controller && \
    chown -R controller:controller /bin/controller /etc/envoy-ingress-controller

# Install libcap2-bin for setcap command
RUN apt-get update && \
    apt-get install -y libcap2-bin && \
    rm -rf /var/lib/apt/lists/*

# Set necessary capabilities for Envoy
RUN setcap "cap_net_bind_service=+ep" /usr/local/bin/envoy

# Switch to non-root user
USER controller

# Set the entrypoint
ENTRYPOINT ["/bin/controller"]
