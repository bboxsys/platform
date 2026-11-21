package client

import (
	"errors"
	"fmt"
)

var (
	ENDPOINT_KINDS = []string{
		"login_service",
		"admin_service",
		"proxy_service",
		"messenger_service",
		"blackbox_service",
		"media_service",
		"queue_service",
		"controller_service",
		"host_service",
		"comms_service",
		"proxy",
	}
)

func EndpointKinds() []string {
	return ENDPOINT_KINDS
}

func InKind(kind string) bool {
	for _, k := range ENDPOINT_KINDS {
		if kind == k {
			return true
		}
	}
	return false
}

type EndpointHealth struct {
}

type ServiceEndpoint struct {
	Kind      string         `json:"kind"`
	URL       string         `json:"url"`
	Health    EndpointHealth `json:"health"`
	ProxyHost string         `json:"proxy_host,omitempty"`
	ProxyPort int            `json:"proxy_port,omitempty"`
}

func NewServiceEndpoint(kind string, url string) ServiceEndpoint {
	var serviceEndpoint ServiceEndpoint
	serviceEndpoint.Kind = kind
	serviceEndpoint.URL = url
	return serviceEndpoint
}

type ServiceEndpoints []ServiceEndpoint

func (serviceEndpoints *ServiceEndpoints) AddEndpoint(kind string, url string) error {
	var endpoint ServiceEndpoint
	if InKind(kind) {
		endpoint.Kind = kind
		endpoint.URL = url
		*serviceEndpoints = append(*serviceEndpoints, endpoint)
		return nil
	}
	return errors.New(fmt.Sprintf("kind not recognized: %s", kind))
}

func (serviceEndpoints *ServiceEndpoints) ByKind(kind string) *[]ServiceEndpoint {
	endpoints := make([]ServiceEndpoint, 0)
	for _, endpoint := range *serviceEndpoints {
		if endpoint.Kind == kind {
			endpoints = append(endpoints, endpoint)
		}
	}
	return &endpoints
}
