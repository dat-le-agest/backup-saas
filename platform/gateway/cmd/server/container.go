package main

import (
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/di"
	"gateway/internal/handler"
)

// buildContainer wires handlers to upstream clients.
// Clients are built in main from rpcc components (or by tests against fake upstreams).
func buildContainer(iam iamv1connect.IamServiceClient) (*di.Container, error) {
	c := di.New()

	steps := []func() error{
		// upstream clients
		func() error { return di.Instance[iamv1connect.IamServiceClient](c, iam) },

		// handlers
		func() error { return di.Provide[*handler.IamHandler](c, handler.NewIamHandler, di.Singleton) },
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return c, c.Build()
}
