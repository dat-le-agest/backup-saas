// Package authzc holds how a service authorizes requests.
//
//	--authz-enforce / AUTHZ_ENFORCE
//	  rbac  (default) the service's static RBAC policy decides
//	  rebac the service's embedded ReBAC engine decides (core/authz/rebac)
//
// The engine records relationships whatever the setting, so switching to rebac
// needs no backfill.
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
		"who decides permissions: rbac (local policy) | rebac (embedded ReBAC engine) - Default rbac")
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
