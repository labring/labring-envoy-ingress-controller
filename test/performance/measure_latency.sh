#!/bin/bash

# Function to measure time until ingress is ready
measure_ingress_latency() {
    local start_time=$(date +%s.%N)
    local ingress_name=$1
    local namespace=$2
    local max_wait=30  # Maximum wait time in seconds
    local ready=false

    while [ $(echo "$(date +%s.%N) - $start_time" | bc) -lt $max_wait ]; do
        if kubectl get ingress $ingress_name -n $namespace &>/dev/null; then
            local address=$(kubectl get ingress $ingress_name -n $namespace -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
            if [ ! -z "$address" ]; then
                ready=true
                break
            fi
        fi
        sleep 0.1
    done

    if [ "$ready" = true ]; then
        local end_time=$(date +%s.%N)
        local duration=$(echo "$end_time - $start_time" | bc)
        echo "$duration"
    else
        echo "timeout"
    fi
}

# Create test namespace
kubectl create namespace perf-test

# Generate test ingress
cat <<EOF | kubectl apply -f -
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: perf-test-ingress
  namespace: perf-test
spec:
  rules:
  - host: perf-test.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: perf-test-service
            port:
              number: 80
EOF

# Measure latency
latency=$(measure_ingress_latency perf-test-ingress perf-test)

if [ "$latency" = "timeout" ]; then
    echo "Test failed: Ingress did not become ready within timeout"
    exit 1
else
    echo "Ingress creation latency: ${latency}s"
    if (( $(echo "$latency > 5" | bc -l) )); then
        echo "Test failed: Latency exceeds 5 seconds requirement"
        exit 1
    else
        echo "Test passed: Latency within 5 seconds requirement"
    fi
fi

# Clean up
kubectl delete namespace perf-test
