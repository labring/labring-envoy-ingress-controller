package main

import (
	"flag"
	"fmt"
	"os"
	"text/template"
)

const ingressTemplate = `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: test-ingress-{{.Index}}
  namespace: test-ns-{{.Namespace}}
spec:
  rules:
  - host: test-{{.Index}}.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: test-service-{{.Index}}
            port:
              number: 80
`

type IngressData struct {
	Index     int
	Namespace int
}

func main() {
	count := flag.Int("count", 100000, "Number of ingress resources to generate")
	nsCount := flag.Int("namespaces", 10, "Number of namespaces to distribute ingresses across")
	flag.Parse()

	tmpl, err := template.New("ingress").Parse(ingressTemplate)
	if err != nil {
		fmt.Printf("Failed to parse template: %v\n", err)
		os.Exit(1)
	}

	// Create output directory
	err = os.MkdirAll("generated", 0755)
	if err != nil {
		fmt.Printf("Failed to create output directory: %v\n", err)
		os.Exit(1)
	}

	// Generate namespace manifests
	nsFile, err := os.Create("generated/00-namespaces.yaml")
	if err != nil {
		fmt.Printf("Failed to create namespace file: %v\n", err)
		os.Exit(1)
	}
	defer nsFile.Close()

	for i := 0; i < *nsCount; i++ {
		fmt.Fprintf(nsFile, `
apiVersion: v1
kind: Namespace
metadata:
  name: test-ns-%d
---
`, i)
	}

	// Generate ingress manifests in batches
	batchSize := 1000
	for i := 0; i < *count; i += batchSize {
		filename := fmt.Sprintf("generated/%02d-ingresses.yaml", i/batchSize)
		f, err := os.Create(filename)
		if err != nil {
			fmt.Printf("Failed to create file %s: %v\n", filename, err)
			os.Exit(1)
		}

		for j := 0; j < batchSize && i+j < *count; j++ {
			data := IngressData{
				Index:     i + j,
				Namespace: (i + j) % *nsCount,
			}
			if err := tmpl.Execute(f, data); err != nil {
				fmt.Printf("Failed to execute template: %v\n", err)
				os.Exit(1)
			}
			fmt.Fprintln(f, "---")
		}
		f.Close()
	}

	fmt.Printf("Generated %d ingress resources across %d namespaces\n", *count, *nsCount)
}
