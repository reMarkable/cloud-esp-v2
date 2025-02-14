// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package filterconfig

import (
	"fmt"
	"strings"

	ci "github.com/GoogleCloudPlatform/esp-v2/src/go/configinfo"
	"github.com/GoogleCloudPlatform/esp-v2/src/go/options"
	commonpb "github.com/GoogleCloudPlatform/esp-v2/src/go/proto/api/envoy/v11/http/common"
	scpb "github.com/GoogleCloudPlatform/esp-v2/src/go/proto/api/envoy/v11/http/service_control"
	"github.com/GoogleCloudPlatform/esp-v2/src/go/util"
	"github.com/GoogleCloudPlatform/esp-v2/src/go/util/httppattern"
	hcmpb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/golang/protobuf/ptypes"
	anypb "github.com/golang/protobuf/ptypes/any"
	wrapperspb "github.com/golang/protobuf/ptypes/wrappers"
	confpb "google.golang.org/genproto/googleapis/api/serviceconfig"
)

var scPerRouteFilterConfigGen = func(method *ci.MethodInfo, httpRule *httppattern.Pattern) (*anypb.Any, error) {
	scPerRoute := &scpb.PerRouteFilterConfig{
		OperationName: method.Operation(),
	}
	scpr, err := ptypes.MarshalAny(scPerRoute)
	if err != nil {
		return nil, fmt.Errorf("error marshaling service_control per-route config to Any: %v", err)
	}
	return scpr, nil
}

var scFilterGenFunc = func(serviceInfo *ci.ServiceInfo) (*hcmpb.HttpFilter, []*ci.MethodInfo, error) {
	if serviceInfo == nil || serviceInfo.ServiceConfig().GetControl().GetEnvironment() == "" {
		return nil, nil, nil
	}

	// TODO(b/148638212): Clean up this hacky way of specifying the protocol for Service Control report.
	// This is safe (for now) as our Service Control filter only differentiates between gRPC or non-gRPC.
	var protocol string
	if serviceInfo.GrpcSupportRequired {
		protocol = "grpc"
	} else {
		// TODO(b/148638212): Must be http1 (not http) for current filter implementation.
		protocol = "http1"
	}

	serviceName := serviceInfo.ServiceConfig().GetName()
	service := &scpb.Service{
		ServiceName:       serviceName,
		ServiceConfigId:   serviceInfo.ConfigID,
		ProducerProjectId: serviceInfo.ServiceConfig().GetProducerProjectId(),
		ServiceConfig:     copyServiceConfigForReportMetrics(serviceInfo.ServiceConfig()),
		BackendProtocol:   protocol,
	}

	if serviceInfo.Options.LogRequestHeaders != "" {
		service.LogRequestHeaders = strings.Split(serviceInfo.Options.LogRequestHeaders, ",")
		for i := range service.LogRequestHeaders {
			service.LogRequestHeaders[i] = strings.TrimSpace(service.LogRequestHeaders[i])
		}
	}
	if serviceInfo.Options.LogResponseHeaders != "" {
		service.LogResponseHeaders = strings.Split(serviceInfo.Options.LogResponseHeaders, ",")
		for i := range service.LogResponseHeaders {
			service.LogResponseHeaders[i] = strings.TrimSpace(service.LogResponseHeaders[i])
		}
	}
	if serviceInfo.Options.LogJwtPayloads != "" {
		service.LogJwtPayloads = strings.Split(serviceInfo.Options.LogJwtPayloads, ",")
		for i := range service.LogJwtPayloads {
			service.LogJwtPayloads[i] = strings.TrimSpace(service.LogJwtPayloads[i])
		}
	}
	if serviceInfo.Options.MinStreamReportIntervalMs != 0 {
		service.MinStreamReportIntervalMs = serviceInfo.Options.MinStreamReportIntervalMs
	}
	service.JwtPayloadMetadataName = util.JwtPayloadMetadataName
	filterConfig := &scpb.FilterConfig{
		Services:        []*scpb.Service{service},
		ScCallingConfig: makeServiceControlCallingConfig(serviceInfo.Options),
		ServiceControlUri: &commonpb.HttpUri{
			Uri:     serviceInfo.ServiceControlURI,
			Cluster: util.ServiceControlClusterName,
			Timeout: ptypes.DurationProto(serviceInfo.Options.HttpRequestTimeout),
		},
		GeneratedHeaderPrefix: serviceInfo.Options.GeneratedHeaderPrefix,
	}

	if serviceInfo.Options.ServiceControlCredentials != nil {
		// Use access token fetched from Google Cloud IAM Server to talk to Service Controller
		filterConfig.AccessToken = &scpb.FilterConfig_IamToken{
			IamToken: &commonpb.IamTokenInfo{
				IamUri: &commonpb.HttpUri{
					Uri:     fmt.Sprintf("%s%s", serviceInfo.Options.IamURL, util.IamAccessTokenPath(serviceInfo.Options.ServiceControlCredentials.ServiceAccountEmail)),
					Cluster: util.IamServerClusterName,
					Timeout: ptypes.DurationProto(serviceInfo.Options.HttpRequestTimeout),
				},
				ServiceAccountEmail: serviceInfo.Options.ServiceControlCredentials.ServiceAccountEmail,
				Delegates:           serviceInfo.Options.ServiceControlCredentials.Delegates,
				AccessToken:         serviceInfo.AccessToken,
			},
		}
	} else {
		filterConfig.AccessToken = &scpb.FilterConfig_ImdsToken{
			ImdsToken: serviceInfo.AccessToken.GetRemoteToken(),
		}

	}

	if serviceInfo.GcpAttributes != nil {
		filterConfig.GcpAttributes = serviceInfo.GcpAttributes
	}
	if serviceInfo.Options.ComputePlatformOverride != "" {
		if filterConfig.GcpAttributes == nil {
			filterConfig.GcpAttributes = &scpb.GcpAttributes{}
		}
		filterConfig.GcpAttributes.Platform = serviceInfo.Options.ComputePlatformOverride
	}

	var perRouteConfigRequiredMethods []*ci.MethodInfo
	for _, operation := range serviceInfo.Operations {
		method := serviceInfo.Methods[operation]
		requirement := &scpb.Requirement{
			ServiceName:        serviceName,
			OperationName:      operation,
			ApiName:            method.ApiName,
			ApiVersion:         method.ApiVersion,
			SkipServiceControl: method.SkipServiceControl,
			MetricCosts:        method.MetricCosts,
		}

		// For these OPTIONS methods, auth should be disabled and AllowWithoutApiKey
		// should be true for each CORS.
		if method.IsGenerated || method.AllowUnregisteredCalls {
			requirement.ApiKey = &scpb.ApiKeyRequirement{
				AllowWithoutApiKey: true,
			}
		}

		if method.ApiKeyLocations != nil {
			if requirement.ApiKey == nil {
				requirement.ApiKey = &scpb.ApiKeyRequirement{}
			}
			requirement.ApiKey.Locations = method.ApiKeyLocations
		}

		perRouteConfigRequiredMethods = append(perRouteConfigRequiredMethods, method)
		filterConfig.Requirements = append(filterConfig.Requirements, requirement)
	}

	depErrorBehaviorEnum, err := parseDepErrorBehavior(serviceInfo.Options.DependencyErrorBehavior)
	if err != nil {
		return nil, nil, err
	}
	filterConfig.DepErrorBehavior = depErrorBehaviorEnum

	scs, err := ptypes.MarshalAny(filterConfig)
	if err != nil {
		return nil, nil, err
	}
	filter := &hcmpb.HttpFilter{
		Name:       util.ServiceControl,
		ConfigType: &hcmpb.HttpFilter_TypedConfig{TypedConfig: scs},
	}
	return filter, perRouteConfigRequiredMethods, nil
}

func makeServiceControlCallingConfig(opts options.ConfigGeneratorOptions) *scpb.ServiceControlCallingConfig {
	setting := &scpb.ServiceControlCallingConfig{}
	setting.NetworkFailOpen = &wrapperspb.BoolValue{Value: opts.ServiceControlNetworkFailOpen}

	if opts.ScCheckTimeoutMs > 0 {
		setting.CheckTimeoutMs = &wrapperspb.UInt32Value{Value: uint32(opts.ScCheckTimeoutMs)}
	}
	if opts.ScQuotaTimeoutMs > 0 {
		setting.QuotaTimeoutMs = &wrapperspb.UInt32Value{Value: uint32(opts.ScQuotaTimeoutMs)}
	}
	if opts.ScReportTimeoutMs > 0 {
		setting.ReportTimeoutMs = &wrapperspb.UInt32Value{Value: uint32(opts.ScReportTimeoutMs)}
	}

	if opts.ScCheckRetries > -1 {
		setting.CheckRetries = &wrapperspb.UInt32Value{Value: uint32(opts.ScCheckRetries)}
	}
	if opts.ScQuotaRetries > -1 {
		setting.QuotaRetries = &wrapperspb.UInt32Value{Value: uint32(opts.ScQuotaRetries)}
	}
	if opts.ScReportRetries > -1 {
		setting.ReportRetries = &wrapperspb.UInt32Value{Value: uint32(opts.ScReportRetries)}
	}
	return setting
}

func copyServiceConfigForReportMetrics(src *confpb.Service) *confpb.Service {
	// Logs and metrics fields are needed by the Envoy HTTP filter
	// to generate proper Metrics for Report calls.
	return &confpb.Service{
		Logs:               src.GetLogs(),
		Metrics:            src.GetMetrics(),
		MonitoredResources: src.GetMonitoredResources(),
		Monitoring:         src.GetMonitoring(),
		Logging:            src.GetLogging(),
	}
}
