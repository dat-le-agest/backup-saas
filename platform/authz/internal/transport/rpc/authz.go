// Package grpc exposes the authz service over gRPC (Connect).
package grpc

import (
	"context"
	"net/http"

	authzv1 "api/gen/go/platform/authz/v1"
	"api/gen/go/platform/authz/v1/authzv1connect"
	"authz/internal/service"
	"core/apperr"
	"core/components/ginc"
	"core/logger"
	"core/rpc"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ authzv1connect.AuthzServiceHandler = (*AuthzHandler)(nil)

type AuthzHandler struct {
	svc    *service.AuthzService
	logger logger.Logger
}

func NewAuthzHandler(svc *service.AuthzService, l logger.Logger) *AuthzHandler {
	return &AuthzHandler{svc: svc, logger: l.With("handler", "authz")}
}

// Mount registers the handler on the router under /platform.authz.v1.AuthzService/.
func (h *AuthzHandler) Mount(r gin.IRouter) {
	path, handler := authzv1connect.NewAuthzServiceHandler(h, rpc.HandlerOptions(h.logger))
	ginc.Mount(r, path, handler)
}

func (h *AuthzHandler) Check(ctx context.Context, req *authzv1.CheckRequest) (*authzv1.CheckResponse, error) {
	ok, err := h.svc.Check(ctx, req.GetApp(), req.GetObject(), req.GetRelation(), req.GetSubject(), req.GetAuthorizationModelId(), consistency(req.GetConsistency()))
	if err != nil {
		return nil, err
	}
	return &authzv1.CheckResponse{Allowed: ok}, nil
}

func (h *AuthzHandler) BatchCheck(ctx context.Context, req *authzv1.BatchCheckRequest) (*authzv1.BatchCheckResponse, error) {
	checks := make([]service.CheckInput, len(req.GetChecks()))
	for i, c := range req.GetChecks() {
		checks[i] = service.CheckInput{Object: c.GetObject(), Relation: c.GetRelation(), Subject: c.GetSubject()}
	}
	results, err := h.svc.BatchCheck(ctx, req.GetApp(), checks, req.GetAuthorizationModelId(), consistency(req.GetConsistency()))
	if err != nil {
		return nil, err
	}
	res := &authzv1.BatchCheckResponse{Results: make([]*authzv1.BatchCheckResult, len(results))}
	for i, r := range results {
		res.Results[i] = &authzv1.BatchCheckResult{Allowed: r.Allowed}
		if r.Err != nil {
			res.Results[i].Error = h.checkError(ctx, i, r.Err)
		}
	}
	return res, nil
}

// checkError: the public form of a failed check; internal errors are logged
// here since the request itself succeeds.
func (h *AuthzHandler) checkError(ctx context.Context, i int, err error) *authzv1.CheckError {
	status, msg := apperr.Public(err)
	if status >= http.StatusInternalServerError {
		logger.WithContext(ctx, h.logger).Errorf("batch check %d: %v", i, err)
	}
	return &authzv1.CheckError{Code: apperr.Code(err), Message: msg}
}

func (h *AuthzHandler) ListObjects(ctx context.Context, req *authzv1.ListObjectsRequest) (*authzv1.ListObjectsResponse, error) {
	objects, err := h.svc.ListObjects(ctx, req.GetApp(), req.GetType(), req.GetRelation(), req.GetSubject(), int(req.GetLimit()), req.GetAuthorizationModelId(), consistency(req.GetConsistency()))
	if err != nil {
		return nil, err
	}
	return &authzv1.ListObjectsResponse{Objects: objects}, nil
}

func (h *AuthzHandler) WriteTuples(ctx context.Context, req *authzv1.WriteTuplesRequest) (*authzv1.WriteTuplesResponse, error) {
	if err := h.svc.WriteTuples(ctx, req.GetApp(), toInputs(req.GetWrites()), toInputs(req.GetDeletes()), req.GetDeleteObjects()); err != nil {
		return nil, err
	}
	return &authzv1.WriteTuplesResponse{}, nil
}

func (h *AuthzHandler) ReadChanges(ctx context.Context, req *authzv1.ReadChangesRequest) (*authzv1.ReadChangesResponse, error) {
	changes, last, err := h.svc.ReadChanges(ctx, req.GetApp(), req.GetObjectType(), req.GetAfterSeq(), int(req.GetLimit()))
	if err != nil {
		return nil, err
	}
	res := &authzv1.ReadChangesResponse{LastSeq: last, Changes: make([]*authzv1.TupleChange, 0, len(changes))}
	for _, c := range changes {
		res.Changes = append(res.Changes, &authzv1.TupleChange{
			Seq:       c.Seq,
			Operation: c.Operation,
			Tuple: &authzv1.Tuple{
				Object: c.Tuple.Object.String(), Relation: c.Tuple.Relation,
				Subject: c.Tuple.Subject.String(), TenantId: c.Tuple.TenantID,
			},
			Actor:      c.Actor,
			ChangeTime: timestamppb.New(c.ChangedAt),
		})
	}
	return res, nil
}

func (h *AuthzHandler) ActivateAuthorizationModel(ctx context.Context, req *authzv1.ActivateAuthorizationModelRequest) (*authzv1.ActivateAuthorizationModelResponse, error) {
	if err := h.svc.ActivateModel(ctx, req.GetApp(), req.GetId()); err != nil {
		return nil, err
	}
	return &authzv1.ActivateAuthorizationModelResponse{}, nil
}

func (h *AuthzHandler) GetAuthorizationModel(ctx context.Context, req *authzv1.GetAuthorizationModelRequest) (*authzv1.GetAuthorizationModelResponse, error) {
	m, err := h.svc.GetModel(ctx, req.GetApp(), req.GetId())
	if err != nil {
		return nil, err
	}
	return &authzv1.GetAuthorizationModelResponse{
		Id: m.ID, Dsl: m.DSL, Active: m.IsActive, CreateTime: timestamppb.New(m.CreatedAt),
	}, nil
}

func consistency(c authzv1.Consistency) service.Consistency {
	if c == authzv1.Consistency_CONSISTENCY_HIGHER {
		return service.Higher
	}
	return service.MinimizeLatency
}

func toInputs(in []*authzv1.Tuple) []service.TupleInput {
	out := make([]service.TupleInput, 0, len(in))
	for _, t := range in {
		out = append(out, service.TupleInput{
			Object: t.GetObject(), Relation: t.GetRelation(), Subject: t.GetSubject(), TenantID: t.GetTenantId(),
		})
	}
	return out
}
