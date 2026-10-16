// Package tenancy enforces tenant isolation at the data layer, twice:
//
//  1. Scope adds "tenant_id = <caller's tenant>" to every repository query.
//
//  2. core/uow sets the Postgres setting app.tenant_id (SetLocal) for each
//     transaction, which a row-level security policy on each tenant table checks:
//
//     ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
//     ALTER TABLE notes FORCE ROW LEVEL SECURITY;
//     CREATE POLICY tenant_isolation ON notes
//     USING (tenant_id = current_setting('app.tenant_id', true))
//     WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
//
// so a query that forgets Scope still can't see or write another tenant's rows.
// Repositories access tenant tables through the unit of work:
//
//	err := r.uow.Tx(ctx, func(tx *gorm.DB) error {
//		return tx.Scopes(tenancy.Scope(ctx)).First(&note, id).Error
//	})
//
// Without a tenant in ctx both layers fail closed. RLS does not apply to
// superusers or roles with BYPASSRLS: services must connect as a plain role.
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

// SetLocal makes row-level security see tenantID for the rest of the
// transaction tx (set_config(..., is_local=true): the setting ends with the
// transaction, so it never leaks to the next request on a pooled connection).
// core/uow calls it at the start of every unit of work that has a tenant.
func SetLocal(tx *gorm.DB, tenantID string) error {
	if err := tx.Exec("SELECT set_config(?, ?, true)", Setting, tenantID).Error; err != nil {
		return fmt.Errorf("set tenant for row-level security: %w", err)
	}
	return nil
}

// Setting is the Postgres setting read by row-level security policies.
const Setting = "app.tenant_id"

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
