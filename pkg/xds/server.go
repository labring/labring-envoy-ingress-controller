package xds

import (
	"context"
	"fmt"
	"net"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
)

type EnvoyXdsServer struct {
	server server.Server
	cache  cache.SnapshotCache
}

func NewXdsServer() *EnvoyXdsServer {
	cache := cache.NewSnapshotCache(false, cache.IDHash{}, nil)
	srv := server.NewServer(context.Background(), cache, nil)
	return &EnvoyXdsServer{
		server: srv,
		cache:  cache,
	}
}

func (s *EnvoyXdsServer) Start(address string) error {
	grpcServer := grpc.NewServer()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}

	// Register all xDS services
	resource.RegisterRouteDiscoveryServiceServer(grpcServer, s.server)
	resource.RegisterEndpointDiscoveryServiceServer(grpcServer, s.server)
	resource.RegisterClusterDiscoveryServiceServer(grpcServer, s.server)
	resource.RegisterListenerDiscoveryServiceServer(grpcServer, s.server)

	return grpcServer.Serve(listener)
}

func (s *EnvoyXdsServer) UpdateConfig(nodeID string, clusters []*cluster.Cluster, routes []*route.RouteConfiguration) error {
	// Create a listener with HTTP connection manager
	routerConfig, err := anypb.New(&router.Router{})
	if err != nil {
		return fmt.Errorf("failed to marshal router config: %v", err)
	}

	manager := &hcm.HttpConnectionManager{
		CodecType:  hcm.HttpConnectionManager_AUTO,
		StatPrefix: "ingress_http",
		RouteSpecifier: &hcm.HttpConnectionManager_RouteConfig{
			RouteConfig: routes[0],
		},
		HttpFilters: []*hcm.HttpFilter{{
			Name: wellknown.Router,
			ConfigType: &hcm.HttpFilter_TypedConfig{
				TypedConfig: routerConfig,
			},
		}},
	}

	pbst, err := anypb.New(manager)
	if err != nil {
		return fmt.Errorf("failed to marshal connection manager: %v", err)
	}

	listener := &listener.Listener{
		Name: "http_listener",
		Address: &core.Address{
			Address: &core.Address_SocketAddress{
				SocketAddress: &core.SocketAddress{
					Protocol: core.SocketAddress_TCP,
					Address:  "0.0.0.0",
					PortSpecifier: &core.SocketAddress_PortValue{
						PortValue: 80,
					},
				},
			},
		},
		FilterChains: []*listener.FilterChain{{
			Filters: []*listener.Filter{{
				Name: wellknown.HTTPConnectionManager,
				ConfigType: &listener.Filter_TypedConfig{
					TypedConfig: pbst,
				},
			}},
		}},
	}

	// Create snapshot with all resources
	snap, err := cache.NewSnapshot("1",
		map[resource.Type][]types.Resource{
			resource.ClusterType:  convertToResource(clusters),
			resource.RouteType:    convertToResource(routes),
			resource.ListenerType: {listener},
		})
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %v", err)
	}

	// Set the snapshot for the node
	err = s.cache.SetSnapshot(context.Background(), nodeID, snap)
	if err != nil {
		return fmt.Errorf("failed to set snapshot: %v", err)
	}

	return nil
}

func convertToResource(resources interface{}) []types.Resource {
	switch v := resources.(type) {
	case []*cluster.Cluster:
		result := make([]types.Resource, len(v))
		for i, r := range v {
			result[i] = r
		}
		return result
	case []*route.RouteConfiguration:
		result := make([]types.Resource, len(v))
		for i, r := range v {
			result[i] = r
		}
		return result
	default:
		return nil
	}
}
