package client

import (
	"api/gen/go/iam/v1/iamv1connect"
	"core/components/rpcc"
)

// NewIam returns a gRPC client for the iam service (tokens, tenants, members).
func NewIam(cc rpcc.ClientComponent) iamv1connect.IamServiceClient {
	return rpcc.New(cc, iamv1connect.NewIamServiceClient)
}
