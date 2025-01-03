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

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
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
		var listeners []types.Resource
		var clusters []types.Resource
		var routes []types.Resource
		var endpoints []types.Resource

		for _, ingress := range r.updateQueue {
			// Process each ingress rule
			for _, rule := range ingress.Spec.Rules {
				// Create a unique name for this host
				name := fmt.Sprintf("%s-%s", ingress.Namespace, rule.Host)

				// Skip rules without HTTP configuration
				if rule.HTTP == nil {
					continue
				}

				// Create endpoints for each backend
				for _, path := range rule.HTTP.Paths {
					backend := path.Backend
					svcName := fmt.Sprintf("%s-%s", name, backend.Service.Name)
					
					// Create endpoint with uint32 conversion
					ep := xds.CreateEndpoint(svcName,
						[]string{fmt.Sprintf("%s.%s.svc.cluster.local", backend.Service.Name, ingress.Namespace)},
						[]uint32{uint32(backend.Service.Port.Number)})
					endpoints = append(endpoints, ep)

					// Create cluster
					cluster := xds.CreateCluster(svcName, ep.Endpoints[0].LbEndpoints)
					clusters = append(clusters, cluster)

					// Create route configuration with proper type assertion
					route := xds.CreateRoute(name, []string{rule.Host}, []*clusterv3.Cluster{cluster.(*clusterv3.Cluster)})
					routes = append(routes, route)
				}

				// Create listener for the host
				listener := xds.CreateListener(name, "0.0.0.0", 80, name)
				listeners = append(listeners, listener)
			}
		}

		// Update xDS server with new configuration
		if err := r.XDSServer.UpdateConfig("ingress", listeners, clusters, routes, endpoints); err != nil {
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
