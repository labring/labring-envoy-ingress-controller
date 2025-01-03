# Labring Envoy Ingress Controller

A high-performance Kubernetes ingress controller using Envoy as the data plane, designed for handling large-scale ingress configurations with minimal latency.

## Features

- Fast ingress rule application (<5s with 100k+ rules)
- Efficient resource utilization
- High stability with multiple replica support
- Envoy-based data plane with xDS protocol
- Optimized for large-scale deployments

## Building the Project

### Prerequisites

- Go 1.21 or later
- Docker
- Kubernetes cluster (v1.24+)

### Build Steps

1. Clone the repository:
```bash
git clone https://github.com/labring/labring-envoy-ingress-controller.git
cd labring-envoy-ingress-controller
```

2. Build the controller binary:
```bash
go mod download
go build -o bin/controller cmd/controller/main.go
```

3. Build the Docker image:
```bash
docker build -t labring/envoy-ingress-controller:latest .
```

## Running the Controller

1. Deploy the controller to your Kubernetes cluster:
```bash
kubectl apply -f deployments/deployment.yaml
```

2. Verify the deployment:
```bash
kubectl get pods -n kube-system -l app=envoy-ingress-controller
```

3. Check controller logs:
```bash
kubectl logs -n kube-system -l app=envoy-ingress-controller
```

## Functional Testing

1. Deploy a test application:
```bash
# Deploy test application
kubectl create deployment web --image=nginx
kubectl expose deployment web --port=80

# Create test ingress
kubectl create ingress web-ingress --rule="example.com/*=web:80"
```

2. Verify ingress configuration:
```bash
# Check ingress status
kubectl get ingress web-ingress

# Test access (update hostname as needed)
curl -H "Host: example.com" http://<ingress-ip>/
```

## Performance Testing

The project includes tools for performance testing under `test/performance/`:

1. Generate test ingress rules:
```bash
# Build the test tool
go build -o bin/generate-ingress test/performance/generate_ingress.go

# Generate 100,000 ingress rules across 100 namespaces
./bin/generate-ingress -count 100000 -namespaces 100
```

2. Measure ingress application latency:
```bash
# Run latency test
./test/performance/measure_latency.sh
```

3. Monitor resource usage:
```bash
# Check controller resource usage
kubectl top pod -n kube-system -l app=envoy-ingress-controller
```

### Performance Test Results

The controller is designed to:
- Apply new ingress rules within 5 seconds even with 100,000+ existing rules
- Maintain low resource usage under high load
- Support multiple replicas for high availability

## Contributing

1. Fork the repository
2. Create your feature branch
3. Commit your changes
4. Push to the branch
5. Create a Pull Request

## License

This project is licensed under the Apache License 2.0 - see the LICENSE file for details
