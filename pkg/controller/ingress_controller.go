package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/fanux/envoy-ingress-controller/pkg/xds"
)

// IngressReconciler reconciles a Ingress object
type IngressReconciler struct {
	client.Client
	Log       logr.Logger
	Scheme    *runtime.Scheme
	XDSServer *xds.Server

	// For batching updates
	updateMutex sync.Mutex
	updateQueue map[string]networkingv1.Ingress
	batchTimer *time.Timer
}

const (
	batchWindow = 100 * time.Millisecond
)

func (r *IngressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("ingress", req.NamespacedName)

	// Fetch the Ingress instance
	var ingress networkingv1.Ingress
	if err := r.Get(ctx, req.NamespacedName, &ingress); err != nil {
		log.Error(err, "Unable to fetch Ingress")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Queue the update
	r.queueUpdate(req.String(), ingress)

	return ctrl.Result{}, nil
}

func (r *IngressReconciler) queueUpdate(key string, ingress networkingv1.Ingress) {
	r.updateMutex.Lock()
	defer r.updateMutex.Unlock()

	if r.updateQueue == nil {
		r.updateQueue = make(map[string]networkingv1.Ingress)
	}

	r.updateQueue[key] = ingress

	// Start or reset batch timer
	if r.batchTimer == nil {
		r.batchTimer = time.AfterFunc(batchWindow, r.processBatch)
	} else {
		r.batchTimer.Reset(batchWindow)
	}
}

func (r *IngressReconciler) processBatch() {
	r.updateMutex.Lock()
	defer r.updateMutex.Unlock()

	// Process all queued updates
	if len(r.updateQueue) > 0 {
		batchLog := r.Log.WithValues("batch_size", len(r.updateQueue))
		batchLog.Info("Processing batch of ingress updates")

		// Convert ingress rules to Envoy configuration
		var clusters []*cluster.Cluster
		var endpoints []*endpoint.ClusterLoadAssignment
		var virtualHosts []*route.VirtualHost
		clusterSet := make(map[string]bool)

		for _, ingress := range r.updateQueue {
			// Check if this ingress is managed by this controller
			ingressClass := ingress.Annotations["kubernetes.io/ingress.class"]
			if ingressClass != "envoy" && ingress.Spec.IngressClassName != nil && *ingress.Spec.IngressClassName != "envoy" {
				batchLog.Info("Skipping ingress with different class", 
					"namespace", ingress.Namespace, 
					"name", ingress.Name, 
					"annotation", ingressClass,
					"className", ingress.Spec.IngressClassName)
				continue
			}
			
			batchLog.Info("Processing ingress", 
				"namespace", ingress.Namespace, 
				"name", ingress.Name,
				"rules", len(ingress.Spec.Rules))

			batchLog.Info("Processing ingress", "namespace", ingress.Namespace, "name", ingress.Name)

			// Process each ingress rule
			for _, rule := range ingress.Spec.Rules {
				if rule.HTTP == nil {
					continue
				}

				// Determine domains for this rule
				domains := []string{rule.Host}
				if rule.Host == "" {
					domains = []string{"*"}
				}

				// Process each path and create routes
				var routes []*route.Route
				for _, path := range rule.HTTP.Paths {
					backend := path.Backend
					svcName := fmt.Sprintf("%s_%s_%d", backend.Service.Name, ingress.Namespace, backend.Service.Port.Number)
					svcHost := fmt.Sprintf("%s.%s.svc.cluster.local", backend.Service.Name, ingress.Namespace)
					
					// Create cluster if not already created
					if !clusterSet[svcName] {
						clusterSet[svcName] = true
						
						batchLog.Info("Creating cluster and endpoint", 
							"service", backend.Service.Name,
							"namespace", ingress.Namespace,
							"port", backend.Service.Port.Number,
							"host", svcHost)

						// Create cluster with health checks
						c := &cluster.Cluster{
							Name:                 svcName,
							ConnectTimeout:       durationpb.New(5 * time.Second),
							ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_STRICT_DNS},
							LbPolicy:            cluster.Cluster_ROUND_ROBIN,
							DnsLookupFamily:     cluster.Cluster_V4_ONLY,
							HealthChecks: []*core.HealthCheck{{
								Timeout:            durationpb.New(2 * time.Second),
								Interval:          durationpb.New(5 * time.Second),
								UnhealthyThreshold: wrapperspb.UInt32(3),
								HealthyThreshold:   wrapperspb.UInt32(2),
								HealthChecker: &core.HealthCheck_HttpHealthCheck_{
									HttpHealthCheck: &core.HealthCheck_HttpHealthCheck{
										Path: "/",
									},
								},
							}},
						}
						clusters = append(clusters, c)

						// Create endpoint with circuit breaker
						e := &endpoint.ClusterLoadAssignment{
							ClusterName: svcName,
							Endpoints: []*endpoint.LocalityLbEndpoints{{
								LbEndpoints: []*endpoint.LbEndpoint{{
									HostIdentifier: &endpoint.LbEndpoint_Endpoint{
										Endpoint: &endpoint.Endpoint{
											Address: &core.Address{
												Address: &core.Address_SocketAddress{
													SocketAddress: &core.SocketAddress{
														Protocol: core.SocketAddress_TCP,
														Address:  svcHost,
														PortSpecifier: &core.SocketAddress_PortValue{
															PortValue: uint32(backend.Service.Port.Number),
														},
													},
												},
											},
										},
									},
								}},
							}},
						}
						endpoints = append(endpoints, e)
						
						batchLog.Info("Created cluster and endpoint", "service", svcName, "port", backend.Service.Port.Number)
					}

					// Create route with proper path handling
					route := &route.Route{
						Match: &route.RouteMatch{
							PathSpecifier: &route.RouteMatch_Prefix{
								Prefix: path.Path,
							},
							CaseSensitive: wrapperspb.Bool(false),
						},
						Action: &route.Route_Route{
							Route: &route.RouteAction{
								ClusterSpecifier: &route.RouteAction_Cluster{
									Cluster: svcName,
								},
								// Only rewrite if path is not "/"
								PrefixRewrite: "/",
								Timeout: durationpb.New(15 * time.Second),
								RetryPolicy: &route.RetryPolicy{
									RetryOn:       "connect-failure,refused-stream,unavailable,cancelled,retriable-status-codes",
									NumRetries:    wrapperspb.UInt32(3),
									RetryBackOff: &route.RetryPolicy_RetryBackOff{
										BaseInterval: durationpb.New(100 * time.Millisecond),
										MaxInterval: durationpb.New(1 * time.Second),
									},
									RetryHostPredicate: []*route.RetryPolicy_RetryHostPredicate{{
										Name: "envoy.retry_host_predicates.previous_hosts",
									}},
									HostSelectionRetryMaxAttempts: 3,
									RetryableStatusCodes:          []uint32{503, 502, 500},
								},
								// Use auto_host_rewrite instead of literal host rewrite
								HostRewriteSpecifier: &route.RouteAction_AutoHostRewrite{
									AutoHostRewrite: wrapperspb.Bool(true),
								},
							},
						},
					}
					batchLog.Info("Created route", 
						"path", path.Path,
						"service", svcName,
						"port", backend.Service.Port.Number)
					routes = append(routes, route)
					batchLog.Info("Created route", "path", path.Path, "service", svcName)
				}

				// Create virtual host for the rule
				name := fmt.Sprintf("%s-%s", ingress.Namespace, ingress.Name)
				if rule.Host != "" {
					name = fmt.Sprintf("%s-%s", name, rule.Host)
				}
				virtualHost := &route.VirtualHost{
					Name:    name,
					Domains: domains,
					Routes:  routes,
				}
				virtualHosts = append(virtualHosts, virtualHost)
				batchLog.Info("Created virtual host", "name", name, "domains", domains)
			}
		}

		// Create route configuration with validation
		routeConfig := &route.RouteConfiguration{
			Name:         "ingress_routes",
			VirtualHosts: virtualHosts,
			ValidateClusters: wrapperspb.Bool(true),
		}

		batchLog.Info("Updating Envoy configuration", 
			"virtualHosts", len(virtualHosts),
			"clusters", len(clusters),
			"endpoints", len(endpoints))

		// Update Envoy configuration
		if err := r.XDSServer.CreateSnapshot(clusters, endpoints, []*route.RouteConfiguration{routeConfig}); err != nil {
			batchLog.Error(err, "Failed to update xDS configuration")
			return
		}

		batchLog.Info("Successfully updated Envoy configuration")

		// Clear the queue
		r.updateQueue = make(map[string]networkingv1.Ingress)
	}
}

func (r *IngressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.Ingress{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10, // Adjust based on performance needs
		}).
		Complete(r)
}
