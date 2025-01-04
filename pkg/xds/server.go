package xds

import (
	"context"
	"fmt"
	"time"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	envoy_file_v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/file/v3"
	accesslog "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type Server struct {
	snapshotCache cache.SnapshotCache
	version       int64
}

func NewServer() *Server {
	return &Server{
		snapshotCache: cache.NewSnapshotCache(false, cache.IDHash{}, nil),
		version:       0,
	}
}

func (s *Server) CreateSnapshot(clusters []*cluster.Cluster, endpoints []*endpoint.ClusterLoadAssignment, routes []*route.RouteConfiguration) error {
	// Create a listener with proper connection timeouts and circuit breakers
	manager := &hcm.HttpConnectionManager{
		CodecType:  hcm.HttpConnectionManager_AUTO,
		StatPrefix: "ingress_http",
		RouteSpecifier: &hcm.HttpConnectionManager_Rds{
			Rds: &hcm.Rds{
				ConfigSource: &core.ConfigSource{
					ResourceApiVersion: core.ApiVersion_V3,
					ConfigSourceSpecifier: &core.ConfigSource_Ads{
						Ads: &core.AggregatedConfigSource{},
					},
				},
				RouteConfigName: "ingress_routes",
			},
		},
		HttpFilters: []*hcm.HttpFilter{{
			Name: wellknown.Router,
			ConfigType: &hcm.HttpFilter_TypedConfig{
				TypedConfig: mustMarshalAny(&router.Router{
					SuppressEnvoyHeaders: true,
				}),
			},
		}},
		CommonHttpProtocolOptions: &core.HttpProtocolOptions{
			IdleTimeout: durationpb.New(60 * time.Second),
		},
		UpgradeConfigs: []*hcm.HttpConnectionManager_UpgradeConfig{{
			UpgradeType: "websocket",
		}},
		StreamIdleTimeout: durationpb.New(5 * time.Second),
		AccessLogs: createAccessLog(),
		HttpProtocolOptions: &core.Http2ProtocolOptions{
			MaxConcurrentStreams: wrapperspb.UInt32(1000),
			InitialStreamWindowSize: wrapperspb.UInt32(65536),
			InitialConnectionWindowSize: wrapperspb.UInt32(1048576),
		},
	}

	pbst, err := anypb.New(manager)
	if err != nil {
		return fmt.Errorf("failed to marshal HTTP connection manager: %v", err)
	}

	listener := &listener.Listener{
		Name: "ingress_listener",
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
				Name: "envoy.filters.network.http_connection_manager",
				ConfigType: &listener.Filter_TypedConfig{
					TypedConfig: pbst,
				},
			}},
		}},
		EnableReusePort: wrapperspb.Bool(true),
	}

	// Create snapshot with proper versioning
	s.version++
	version := fmt.Sprintf("v%d", s.version)
	snapshot, err := cache.NewSnapshot(version,
		map[resource.Type][]types.Resource{
			resource.ClusterType:  toResources(clusters),
			resource.EndpointType: toResources(endpoints),
			resource.RouteType:    toResources(routes),
			resource.ListenerType: {listener},
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %v", err)
	}

	return s.snapshotCache.SetSnapshot(context.Background(), "ingress", snapshot)
}

func (s *Server) Cache() cache.SnapshotCache {
	return s.snapshotCache
}

func createAccessLog() []*hcm.AccessLog {
	fileAccessLog := &envoy_file_v3.FileAccessLog{
		Path: "/dev/stdout",
		AccessLogFormat: &envoy_file_v3.FileAccessLog_Format{
			Format: &core.SubstitutionFormatString{
				Format: &core.SubstitutionFormatString_TextFormatSource{
					TextFormatSource: &core.DataSource{
						Specifier: &core.DataSource_InlineString{
							InlineString: "[%START_TIME%] \"%REQ(:METHOD)% %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)% %PROTOCOL%\" %RESPONSE_CODE% %RESPONSE_FLAGS% %BYTES_RECEIVED% %BYTES_SENT% %DURATION% %RESP(X-ENVOY-UPSTREAM-SERVICE-TIME)% \"%REQ(X-FORWARDED-FOR)%\" \"%REQ(USER-AGENT)%\" \"%REQ(X-REQUEST-ID)%\" \"%REQ(:AUTHORITY)%\" \"%UPSTREAM_HOST%\"\n",
						},
					},
				},
			},
		},
	}

	pbst, err := anypb.New(fileAccessLog)
	if err != nil {
		log.Printf("failed to marshal file access log config: %v", err)
		return nil
	}

	return []*hcm.AccessLog{{
		Name: wellknown.FileAccessLog,
		ConfigType: &hcm.AccessLog_TypedConfig{
			TypedConfig: pbst,
		},
	}}
}

func toResources(slice interface{}) []types.Resource {
	switch v := slice.(type) {
	case []*cluster.Cluster:
		resources := make([]types.Resource, len(v))
		for i := range v {
			resources[i] = v[i]
		}
		return resources
	case []*endpoint.ClusterLoadAssignment:
		resources := make([]types.Resource, len(v))
		for i := range v {
			resources[i] = v[i]
		}
		return resources
	case []*route.RouteConfiguration:
		resources := make([]types.Resource, len(v))
		for i := range v {
			resources[i] = v[i]
		}
		return resources
	default:
		return nil
	}
}

func mustMarshalAny(message proto.Message) *anypb.Any {
	if message == nil {
		panic("nil message")
	}
	any, err := anypb.New(message)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal message: %v", err))
	}
	return any
}

func CreateCluster(name string, endpoints []*endpoint.LbEndpoint) *cluster.Cluster {
	return &cluster.Cluster{
		Name:                 name,
		ConnectTimeout:       durationpb.New(5 * time.Second),
		ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_EDS},
		LbPolicy:            cluster.Cluster_ROUND_ROBIN,
		EdsClusterConfig: &cluster.Cluster_EdsClusterConfig{
			EdsConfig: &core.ConfigSource{
				ConfigSourceSpecifier: &core.ConfigSource_Ads{
					Ads: &core.AggregatedConfigSource{},
				},
				ResourceApiVersion: core.ApiVersion_V3,
			},
		},
		HealthChecks: []*core.HealthCheck{{
			Timeout:            durationpb.New(2 * time.Second),
			Interval:           durationpb.New(5 * time.Second),
			UnhealthyThreshold: wrapperspb.UInt32(3),
			HealthyThreshold:   wrapperspb.UInt32(2),
			HealthChecker: &core.HealthCheck_HttpHealthCheck_{
				HttpHealthCheck: &core.HealthCheck_HttpHealthCheck{
					Path: "/healthz",
					Host: "health.local",
				},
			},
		}},
		CircuitBreakers: &cluster.CircuitBreakers{
			Thresholds: []*cluster.CircuitBreakers_Thresholds{{
				Priority:           core.RoutingPriority_DEFAULT,
				MaxConnections:     wrapperspb.UInt32(1000),
				MaxPendingRequests: wrapperspb.UInt32(1000),
				MaxRequests:        wrapperspb.UInt32(1000),
				MaxRetries:         wrapperspb.UInt32(3),
			}},
		},
		OutlierDetection: &cluster.OutlierDetection{
			Consecutive_5Xx:                    wrapperspb.UInt32(5),
			Interval:                           durationpb.New(10 * time.Second),
			BaseEjectionTime:                   durationpb.New(30 * time.Second),
			MaxEjectionPercent:                 wrapperspb.UInt32(10),
			EnforcingConsecutive_5Xx:          wrapperspb.UInt32(100),
			EnforcingSuccessRate:              wrapperspb.UInt32(100),
			SuccessRateMinimumHosts:           wrapperspb.UInt32(5),
			SuccessRateRequestVolume:          wrapperspb.UInt32(100),
			SuccessRateStdevFactor:            wrapperspb.UInt32(1900),
		},
	}
}

func CreateEndpoint(clusterName string, endpoints []*endpoint.LbEndpoint) *endpoint.ClusterLoadAssignment {
	return &endpoint.ClusterLoadAssignment{
		ClusterName: clusterName,
		Endpoints: []*endpoint.LocalityLbEndpoints{{
			LbEndpoints: endpoints,
			Priority:    0,
		}},
	}
}

func CreateRoute(name string, domains []string, routes []*route.Route) *route.RouteConfiguration {
	return &route.RouteConfiguration{
		Name: name,
		VirtualHosts: []*route.VirtualHost{{
			Name:    name,
			Domains: domains,
			Routes:  routes,
		}},
		ValidateClusters: wrapperspb.Bool(true),
	}
}
