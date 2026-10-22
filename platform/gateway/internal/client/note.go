// Package client builds clients for upstream services.
// Handlers depend on the generated client interfaces (e.g. notev1connect.NoteServiceClient),
// so tests can swap in a fake or a client pointing at a test server.
package client

import (
	"api/gen/go/apps/notes/note/v1/notev1connect"
	"core/components/rpcc"
)

// NewNote returns a gRPC client for the note service. Calls carry identity and
// request id from ctx, get a default deadline, and return apperr sentinels on failure.
func NewNote(cc rpcc.ClientComponent) notev1connect.NoteServiceClient {
	return rpcc.New(cc, notev1connect.NewNoteServiceClient)
}
