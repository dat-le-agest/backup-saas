package main

import (
	"api/gen/go/apps/notes/note/v1/notev1connect"
	"core/di"
	"notes-gateway/internal/handler"
)

// buildContainer wires handlers to upstream clients.
// Clients are built in main from rpcc components (or by tests against fake upstreams).
func buildContainer(notes notev1connect.NoteServiceClient) (*di.Container, error) {
	c := di.New()

	steps := []func() error{
		// upstream clients
		func() error { return di.Instance[notev1connect.NoteServiceClient](c, notes) },

		// handlers
		func() error { return di.Provide[*handler.NoteHandler](c, handler.NewNoteHandler, di.Singleton) },
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return c, c.Build()
}
