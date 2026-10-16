package authz

import (
	"context"
	"fmt"
	"sync"
)

// Request is one decision of a batch: may the caller do Action on Resource?
type Request struct {
	Action   string
	Resource Resource
}

// BatchAuthorizer is implemented by Authorizers that decide many requests at
// once, e.g. which buttons to show for each note of a page. Use CheckMany
// rather than asserting it: CheckMany falls back to Check for others.
type BatchAuthorizer interface {
	// CheckMany returns one error per request, in order, as Check would
	// (nil = allowed).
	CheckMany(ctx context.Context, reqs []Request) []error
}

// CheckMany decides reqs with az, in one round trip when az is a
// BatchAuthorizer (ReBAC configured WithBatchChecker), otherwise one Check each.
//
//	errs := authz.CheckMany(ctx, az, []authz.Request{
//		{Action: authz.Update, Resource: authz.Resource{Type: "note", ID: "1", TenantID: t}},
//		{Action: authz.Delete, Resource: authz.Resource{Type: "note", ID: "1", TenantID: t}},
//	})
//	canUpdate, canDelete := errs[0] == nil, errs[1] == nil
func CheckMany(ctx context.Context, az Authorizer, reqs []Request) []error {
	if b, ok := az.(BatchAuthorizer); ok {
		return b.CheckMany(ctx, reqs)
	}
	errs := make([]error, len(reqs))
	for i, r := range reqs {
		errs[i] = az.Check(ctx, r.Action, r.Resource)
	}
	return errs
}

// RBAC decides locally: a batch is a loop.
func (a *rbac) CheckMany(ctx context.Context, reqs []Request) []error {
	errs := make([]error, len(reqs))
	for i, r := range reqs {
		errs[i] = a.Check(ctx, r.Action, r.Resource)
	}
	return errs
}

// MaxBatchChecks is how many relation checks the kit's authz service accepts
// per BatchCheck; larger batches are split.
const MaxBatchChecks = 50

// RelationCheck: does Subject have Relation on Object?
type RelationCheck struct {
	Object, Relation, Subject string
}

// RelationResult answers one RelationCheck; Err when it couldn't be evaluated.
type RelationResult struct {
	Allowed bool
	Err     error
}

// BatchCheckFunc asks the authz service many relation checks at once (its
// BatchCheck), one result per check in order. The error is for the call as a
// whole and denies every check of it. Services adapt their generated client:
//
//	authz.WithBatchChecker(func(ctx context.Context, checks []authz.RelationCheck, c authz.Consistency) ([]authz.RelationResult, error) {
//		res, err := client.BatchCheck(ctx, &authzv1.BatchCheckRequest{Checks: ..., Consistency: ...})
//		...
//	})
type BatchCheckFunc func(ctx context.Context, checks []RelationCheck, c Consistency) ([]RelationResult, error)

// WithBatchChecker makes the ReBAC Authorizer a BatchAuthorizer.
func WithBatchChecker(f BatchCheckFunc) ReBACOption { return func(r *rebac) { r.batch = f } }

// CheckMany guards each request locally, then asks the authz service in calls
// of at most MaxBatchChecks, run concurrently. A batch has one consistency:
// Fresh as soon as one of its requests needs it.
func (r *rebac) CheckMany(ctx context.Context, reqs []Request) []error {
	errs := make([]error, len(reqs))
	if r.batch == nil {
		for i, req := range reqs {
			errs[i] = r.Check(ctx, req.Action, req.Resource)
		}
		return errs
	}

	var (
		queries []query
		at      []int // index in reqs of each query
	)
	consistency := MinimizeLatency
	for i, req := range reqs {
		q, err := r.prepare(ctx, req.Action, req.Resource)
		if err != nil {
			errs[i] = err
			continue
		}
		if q.fresh {
			consistency = Fresh
		}
		queries = append(queries, q)
		at = append(at, i)
	}

	var wg sync.WaitGroup
	for start := 0; start < len(queries); start += MaxBatchChecks {
		end := min(start+MaxBatchChecks, len(queries))
		wg.Add(1)
		go func(chunk []query, at []int) {
			defer wg.Done()
			checks := make([]RelationCheck, len(chunk))
			for i, q := range chunk {
				checks[i] = q.RelationCheck
			}
			results, err := r.batch(ctx, checks, consistency)
			if err == nil && len(results) != len(checks) {
				err = fmt.Errorf("batch check: %d results for %d checks", len(results), len(checks))
			}
			for i, q := range chunk {
				if err != nil { // fail closed
					errs[at[i]] = q.decide(false, err)
				} else {
					errs[at[i]] = q.decide(results[i].Allowed, results[i].Err)
				}
			}
		}(queries[start:end], at[start:end])
	}
	wg.Wait()
	return errs
}
