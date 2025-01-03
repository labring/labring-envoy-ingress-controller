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
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	xds "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	networkingv1 "k8s.io/api/networking/v1"
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

	// Listen on all interfaces for xDS server
	listener, err := net.Listen("tcp", "0.0.0.0:18000")
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
func mustMarshalAny(msg proto.Message) *anypb.Any {
	any, err := anypb.New(msg)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal message to any: %v", err))
	}
	return any
}

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
				Name: wellknown.HTTPConnectionManager,
				ConfigType: &listener.Filter_TypedConfig{
					TypedConfig: mustMarshalAny(&hcm.HttpConnectionManager{
						StatPrefix:  "ingress_http",
						CodecType:  hcm.HttpConnectionManager_AUTO,
						RouteSpecifier: &hcm.HttpConnectionManager_Rds{
							Rds: &hcm.Rds{
								RouteConfigName: routeName,
								ConfigSource: &core.ConfigSource{
									ResourceApiVersion: core.ApiVersion_V3,
									ConfigSourceSpecifier: &core.ConfigSource_Ads{
										Ads: &core.AggregatedConfigSource{},
									},
								},
							},
						},
						HttpFilters: []*hcm.HttpFilter{{
							Name: wellknown.Router,
							ConfigType: &hcm.HttpFilter_TypedConfig{
								TypedConfig: mustMarshalAny(&router.Router{}),
							},
						}},
					}),
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

func CreateRoute(name string, virtualHosts []*route.VirtualHost) *route.RouteConfiguration {
	return &route.RouteConfiguration{
		Name:         name,
		VirtualHosts: virtualHosts,
	}
}

func CreateVirtualHost(name string, domains []string, paths []*networkingv1.HTTPIngressPath) *route.VirtualHost {
	var routes []*route.Route
	for _, path := range paths {
		routes = append(routes, &route.Route{
			Match: &route.RouteMatch{
				PathSpecifier: &route.RouteMatch_Prefix{
					Prefix: path.Path,
				},
			},
			Action: &route.Route_Route{
				Route: &route.RouteAction{
					ClusterSpecifier: &route.RouteAction_Cluster{
						Cluster: fmt.Sprintf("%s-%s", name, path.Backend.Service.Name),
					},
					Timeout: durationpb.New(15 * time.Second),
				},
			},
		})
	}
	
	return &route.VirtualHost{
		Name:    name,
		Domains: domains,
		Routes:  routes,
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
