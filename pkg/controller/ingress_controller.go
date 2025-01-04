package controller

import (
"context"
"fmt"
"time"

route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
"github.com/labring/labring-envoy-ingress-controller/pkg/xds"
networkingv1 "k8s.io/api/networking/v1"
"k8s.io/apimachinery/pkg/runtime"
ctrl "sigs.k8s.io/controller-runtime"
"sigs.k8s.io/controller-runtime/pkg/client"
"sigs.k8s.io/controller-runtime/pkg/log"
)

type IngressReconciler struct {
client.Client
Scheme     *runtime.Scheme
XDSServer  *xds.Server
}

func (r *IngressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
logger := log.FromContext(ctx)
logger.Info("Reconciling Ingress")

var ing networkingv1.Ingress
if err := r.Get(ctx, req.NamespacedName, &ing); err != nil {
return ctrl.Result{}, client.IgnoreNotFound(err)
}

if len(ing.Spec.Rules) == 0 || len(ing.Spec.Rules[0].HTTP.Paths) == 0 {
return ctrl.Result{}, nil
}

var routes []*route.Route
for _, rule := range ing.Spec.Rules {
for _, path := range rule.HTTP.Paths {
clusterName := fmt.Sprintf("%s-%s", ing.Namespace, path.Backend.Service.Name)
route := &route.Route{
Match: &route.RouteMatch{
PathSpecifier: &route.RouteMatch_Prefix{
Prefix: path.Path,
},
},
Action: &route.Route_Route{
Route: &route.RouteAction{
ClusterSpecifier: &route.RouteAction_Cluster{
Cluster: clusterName,
},
PrefixRewrite: "/",
HostRewriteSpecifier: &route.RouteAction_HostRewriteLiteral{
HostRewriteLiteral: fmt.Sprintf("%s.%s.svc.cluster.local", path.Backend.Service.Name, ing.Namespace),
},
},
},
}
routes = append(routes, route)

// Create cluster configuration
err := r.XDSServer.CreateCluster(clusterName, ing.Namespace, path.Backend.Service.Name, path.Backend.Service.Port.Number)
if err != nil {
logger.Error(err, "Failed to create cluster")
return ctrl.Result{RequeueAfter: time.Second * 5}, err
}
}
}

if err := r.XDSServer.CreateSnapshot(routes); err != nil {
logger.Error(err, "Failed to create snapshot")
return ctrl.Result{RequeueAfter: time.Second * 5}, err
}

return ctrl.Result{}, nil
}

func (r *IngressReconciler) SetupWithManager(mgr ctrl.Manager) error {
return ctrl.NewControllerManagedBy(mgr).
For(&networkingv1.Ingress{}).
Complete(r)
}
