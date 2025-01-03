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

	// Generate namespace manifests
	for i := 0; i < *nsCount; i++ {
		fmt.Printf(`
apiVersion: v1
kind: Namespace
metadata:
  name: test-ns-%d
---
`, i)
	}

	// Generate ingress manifests
	for i := 0; i < *count; i++ {
		data := IngressData{
			Index:     i,
			Namespace: i % *nsCount,
		}
		if err := tmpl.Execute(os.Stdout, data); err != nil {
			fmt.Printf("Failed to execute template: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("---")
	}

	fmt.Printf("Generated %d ingress resources across %d namespaces\n", *count, *nsCount)
}
