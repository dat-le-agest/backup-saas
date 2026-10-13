// Package tenancy enforces tenant isolation at the data layer.
//
// Every tenant-owned table has a tenant_id column, and every repository query
// goes through Scope:
//
//	r.db.WithContext(ctx).Scopes(tenancy.Scope(ctx)).First(&note, id)
//
// Without a tenant in ctx the query fails instead of returning every tenant's rows.
package tenancy

import (
	"context"
	"fmt"

	"core/apperr"
	"core/reqctx"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const Column = "tenant_id"

// TenantID returns the caller's tenant: ErrUnauthenticated without identity,
// ErrForbidden if the identity has no tenant selected.
func TenantID(ctx context.Context) (string, error) {
	id, ok := reqctx.IdentityFrom(ctx)
	if !ok {
		return "", fmt.Errorf("%w: no caller identity", apperr.ErrUnauthenticated)
	}
	if id.TenantID == "" {
		return "", fmt.Errorf("%w: no tenant selected", apperr.ErrForbidden)
	}
	return id.TenantID, nil
}

// Scope restricts a query to the caller's tenant. It qualifies the column with
// the current table so it stays correct in joins. If ctx has no tenant, the
// statement gets an error and is never executed (fail closed).
func Scope(ctx context.Context) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		tenantID, err := TenantID(ctx)
		if err != nil {
			_ = db.AddError(err)
			return db
		}
		return db.Where(clause.Eq{
			Column: clause.Column{Table: clause.CurrentTable, Name: Column},
			Value:  tenantID,
		})
	}
}
