// Package authzc holds how a service authorizes requests.
//
//	--authz-enforce / AUTHZ_ENFORCE
//	  rbac  (default) the service's static RBAC policy decides
//	  rebac the central authz service decides (needs AUTHZ_SERVICE_URL)
//
// Syncing relationships to the authz service is separate: it happens whenever
// AUTHZ_SERVICE_URL is set. That allows a safe switch: sync, backfill existing
// data (scripts/authz-backfill.sh), then enforce rebac.
package authzc

import (
	"core/logger"
	"core/sctx"
	"flag"
	"fmt"
)

const (
	EnforceRBAC  = "rbac"
	EnforceReBAC = "rebac"
)

type SettingsComponent interface {
	Enforce() string
}

type settings struct {
	id      string
	enforce string
	logger  logger.Logger
}

func NewSettings(id string) *settings {
	return &settings{id: id}
}

func (s *settings) ID() string { return s.id }

func (s *settings) InitFlags() {
	flag.StringVar(&s.enforce, "authz-enforce", EnforceRBAC,
		"who decides permissions: rbac (local policy) | rebac (central authz service) - Default rbac")
}

func (s *settings) Activate(sc sctx.ServiceContext) error {
	s.logger = sc.Logger(s.id)
	switch s.enforce {
	case EnforceRBAC, EnforceReBAC:
		return nil
	}
	return fmt.Errorf("invalid authz-enforce %q (rbac | rebac)", s.enforce)
}

func (s *settings) Stop() error { return nil }

func (s *settings) Enforce() string { return s.enforce }
