// Package serverhost builds an AI instance for a service on package
// server. It is separate from ai/instance so a host that does not run on
// package server (a host agent, a CLI) can use ai/instance without
// linking the HTTP server, Let's Encrypt, and their dependencies.
package serverhost

import (
	"errors"

	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/server"
)

// New builds an instance for a service on package server, filling what the
// server already knows: Publisher defaults to srv.MessengerClient when the
// server has one, Handler to srv.Srv.Handler, Name and Service to
// srv.ServerOptions.Component, and Logger to srv.Logger. Mount it on
// srv.Router behind the service's auth wrappers before srv.Start.
func New(srv *server.Server, o instance.Options) (*instance.Instance, error) {
	if srv == nil {
		return nil, errors.New("ai/instance/serverhost: server is nil")
	}
	if o.Publisher == nil && srv.MessengerClient != nil {
		o.Publisher = srv.MessengerClient
	}
	if o.Handler == nil && srv.Srv != nil {
		o.Handler = srv.Srv.Handler
	}
	if srv.ServerOptions != nil {
		if o.Name == "" {
			o.Name = srv.ServerOptions.Component
		}
		if o.Service == "" {
			o.Service = srv.ServerOptions.Component
		}
	}
	if o.Logger == nil {
		o.Logger = srv.Logger
	}
	return instance.New(o)
}
