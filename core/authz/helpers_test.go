package authz

import (
	"context"

	"core/reqctx"
)

func as(userID, tenantID, role string) context.Context {
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{AppID: "notes", UserID: userID, TenantID: tenantID, Role: role})
}
