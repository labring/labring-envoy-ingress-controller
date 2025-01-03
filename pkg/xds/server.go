package xds

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	xds "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

type Server struct {
	ctx        context.Context
	server     xds.Server
	cache      cache.SnapshotCache
	grpcServer *grpc.Server
	version    int64
	mutex      sync.Mutex
}

func NewServer(ctx context.Context) *Server {
	cache := cache.NewSnapshotCache(false, cache.IDHash{}, nil)
	srv := xds.NewServer(ctx, cache, nil)
	return &Server{
		ctx:     ctx,
		server:  srv,
		cache:   cache,
		version: 0,
	}
}

func (s *Server) Run(address string) error {
	grpcServer := grpc.NewServer()
	discovery.RegisterAggregatedDiscoveryServiceServer(grpcServer, s.server)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	s.grpcServer = grpcServer
	return grpcServer.Serve(listener)
}

func (s *Server) Stop() {
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
}

func (s *Server) UpdateConfig(nodeID string, listeners []types.Resource, clusters []types.Resource, routes []types.Resource, endpoints []types.Resource) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.version++
	version := fmt.Sprintf("v%d", s.version)

	snapshot, err := cache.NewSnapshot(version,
		map[resource.Type][]types.Resource{
			resource.ListenerType: listeners,
			resource.ClusterType: clusters,
			resource.RouteType:   routes,
			resource.EndpointType: endpoints,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %v", err)
	}

	return s.cache.SetSnapshot(context.Background(), nodeID, snapshot)
}

// Helper functions for creating Envoy resources
func CreateListener(name string, address string, port uint32, routeName string) *listener.Listener {
	return &listener.Listener{
		Name: name,
		Address: &core.Address{
			Address: &core.Address_SocketAddress{
				SocketAddress: &core.SocketAddress{
					Protocol: core.SocketAddress_TCP,
					Address:  address,
					PortSpecifier: &core.SocketAddress_PortValue{
						PortValue: port,
					},
				},
			},
		},
		FilterChains: []*listener.FilterChain{{
			Filters: []*listener.Filter{{
				Name: "envoy.filters.network.http_connection_manager",
				ConfigType: &listener.Filter_TypedConfig{
					TypedConfig: &anypb.Any{
						TypeUrl: "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
						Value: []byte(`{
							"stat_prefix": "ingress_http",
							"route_config": {
								"name": "` + routeName + `",
								"virtual_hosts": []
							},
							"http_filters": [{
								"name": "envoy.filters.http.router"
							}]
						}`),
					},
				},
			}},
		}},
	}
}

func CreateCluster(name string, endpoints []*endpoint.LbEndpoint) *cluster.Cluster {
	return &cluster.Cluster{
		Name:                 name,
		ConnectTimeout:      durationpb.New(5 * time.Second),
		ClusterDiscoveryType: &cluster.Cluster_Type{
			Type: cluster.Cluster_STRICT_DNS,
		},
		LbPolicy:            cluster.Cluster_ROUND_ROBIN,
		LoadAssignment: &endpoint.ClusterLoadAssignment{
			ClusterName: name,
			Endpoints: []*endpoint.LocalityLbEndpoints{{
				LbEndpoints: endpoints,
			}},
		},
	}
}

func CreateRoute(name string, domains []string, clusters []*cluster.Cluster) *route.RouteConfiguration {
	var virtualHosts []*route.VirtualHost
	for i, cluster := range clusters {
		virtualHosts = append(virtualHosts, &route.VirtualHost{
			Name:    fmt.Sprintf("%s-vhost-%d", name, i),
			Domains: domains,
			Routes: []*route.Route{{
				Match: &route.RouteMatch{
					PathSpecifier: &route.RouteMatch_Prefix{
						Prefix: "/",
					},
				},
				Action: &route.Route_Route{
					Route: &route.RouteAction{
						ClusterSpecifier: &route.RouteAction_Cluster{
							Cluster: cluster.Name,
						},
						Timeout: durationpb.New(15 * time.Second),
					},
				},
			}},
		})
	}
	
	return &route.RouteConfiguration{
		Name:         name,
		VirtualHosts: virtualHosts,
	}
}

func CreateEndpoint(clusterName string, addresses []string, ports []uint32) *endpoint.ClusterLoadAssignment {
	var endpoints []*endpoint.LbEndpoint
	for i, addr := range addresses {
		port := uint32(80)
		if i < len(ports) {
			port = ports[i]
		}
		endpoints = append(endpoints, &endpoint.LbEndpoint{
			HostIdentifier: &endpoint.LbEndpoint_Endpoint{
				Endpoint: &endpoint.Endpoint{
					Address: &core.Address{
						Address: &core.Address_SocketAddress{
							SocketAddress: &core.SocketAddress{
								Protocol: core.SocketAddress_TCP,
								Address:  addr,
								PortSpecifier: &core.SocketAddress_PortValue{
									PortValue: port,
								},
							},
						},
					},
				},
			},
		})
	}
	
	return &endpoint.ClusterLoadAssignment{
		ClusterName: clusterName,
		Endpoints: []*endpoint.LocalityLbEndpoints{{
			LbEndpoints: endpoints,
		}},
	}
}
