// Package approvals implements a generic, amount-tiered, multi-step approval
// engine shared by procurement documents (purchase orders, requisitions).
//
// A tenant configures ApprovalRules per module; each rule has an amount band and
// an ordered list of ApprovalSteps (each naming an approver role). When a document
// is submitted, the most specific matching rule generates an ApprovalRequest with
// one pending ApprovalAction per step. Approvers act on the current step in
// sequence; the request is approved only after the final step, or rejected the
// moment any step is rejected.
package approvals

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entact "github.com/bengobox/inventory-service/internal/ent/approvalaction"
	entreq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entrule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
)

// Sentinel errors returned by Act.
var (
	ErrNotPending    = errors.New("approval request is not pending")
	ErrNoCurrentStep = errors.New("no pending step to act on")
	ErrForbiddenRole = errors.New("user lacks the role required for this step")
)

// Decision is the outcome an approver records on a step.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

// Service is the approval engine. It depends only on the Ent client.
type Service struct {
	orm *ent.Client
}

// NewService creates an approval engine bound to the given Ent client.
func NewService(orm *ent.Client) *Service { return &Service{orm: orm} }

// MatchRule returns the active rule for (tenant, module) whose amount band
// contains amount. The most specific match wins (highest min_amount). Returns
// (nil, nil) when no rule matches, meaning no approval is required.
func (s *Service) MatchRule(ctx context.Context, tenantID uuid.UUID, module string, amount float64) (*ent.ApprovalRule, error) {
	rules, err := s.orm.ApprovalRule.Query().
		Where(
			entrule.TenantID(tenantID),
			entrule.ModuleEQ(entrule.Module(module)),
			entrule.IsActive(true),
		).
		WithSteps().
		All(ctx)
	if err != nil {
		return nil, err
	}
	var best *ent.ApprovalRule
	for _, ru := range rules {
		if amount < ru.MinAmount {
			continue
		}
		if ru.MaxAmount != nil && amount > *ru.MaxAmount {
			continue
		}
		if best == nil || ru.MinAmount > best.MinAmount {
			best = ru
		}
	}
	return best, nil
}

// Submit creates an ApprovalRequest (plus a pending ApprovalAction per step) for a
// document, if a rule with steps matches. Any prior pending request for the same
// object is cancelled first. Returns (request, requiresApproval, error); when no
// rule matches requiresApproval is false and request is nil.
func (s *Service) Submit(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, objectRef string, amount float64, submittedBy *uuid.UUID) (*ent.ApprovalRequest, bool, error) {
	return s.SubmitWithPayload(ctx, tenantID, module, objectID, objectRef, amount, submittedBy, nil)
}

// SubmitWithPayload is Submit plus an effecting-action payload persisted on the
// request. The payload is replayed by the approval-execution path when the request
// is approved — used by gates that mutate state only AFTER sign-off (a large stock
// adjustment stashes its sku/qty/warehouse here so approving actually posts the
// stock). Pass nil for documents that persist their own state.
func (s *Service) SubmitWithPayload(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, objectRef string, amount float64, submittedBy *uuid.UUID, payload map[string]any) (*ent.ApprovalRequest, bool, error) {
	rule, err := s.MatchRule(ctx, tenantID, module, amount)
	if err != nil {
		return nil, false, err
	}
	if rule == nil {
		return nil, false, nil
	}
	steps := append([]*ent.ApprovalStep(nil), rule.Edges.Steps...)
	if len(steps) == 0 {
		return nil, false, nil // a rule with no steps imposes no gate
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Sequence < steps[j].Sequence })

	// Supersede any prior open request for this object so there is a single live workflow.
	_, _ = s.orm.ApprovalRequest.Update().
		Where(
			entreq.TenantID(tenantID),
			entreq.ObjectID(objectID),
			entreq.StatusEQ(entreq.StatusPending),
		).
		SetStatus(entreq.StatusCancelled).
		SetDecidedAt(time.Now()).
		Save(ctx)

	rc := s.orm.ApprovalRequest.Create().
		SetTenantID(tenantID).
		SetModule(entreq.Module(module)).
		SetObjectID(objectID).
		SetObjectReference(objectRef).
		SetAmount(amount).
		SetRuleID(rule.ID).
		SetCurrentSequence(steps[0].Sequence)
	if submittedBy != nil {
		rc = rc.SetSubmittedBy(*submittedBy)
	}
	if len(payload) > 0 {
		rc = rc.SetPayload(payload)
	}
	req, err := rc.Save(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, st := range steps {
		if _, err := s.orm.ApprovalAction.Create().
			SetTenantID(tenantID).
			SetRequestID(req.ID).
			SetSequence(st.Sequence).
			SetName(st.Name).
			SetApproverRole(st.ApproverRole).
			Save(ctx); err != nil {
			return nil, false, err
		}
	}
	return req, true, nil
}

// Act records a decision on the request's current step. roleCheck reports whether
// the acting user may approve the step's role (callers should make it always-true
// for admins). On approve, the request advances to the next step or, if none
// remain, becomes approved. On reject, the request becomes rejected immediately.
func (s *Service) Act(ctx context.Context, tenantID, requestID, userID uuid.UUID, decision Decision, comment string, roleCheck func(roleCode string) bool) (*ent.ApprovalRequest, error) {
	req, err := s.orm.ApprovalRequest.Query().
		Where(entreq.ID(requestID), entreq.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if req.Status != entreq.StatusPending {
		return req, ErrNotPending
	}
	act, err := s.orm.ApprovalAction.Query().
		Where(
			entact.RequestID(req.ID),
			entact.SequenceEQ(req.CurrentSequence),
			entact.StatusEQ(entact.StatusPending),
		).
		Only(ctx)
	if err != nil {
		return nil, ErrNoCurrentStep
	}
	if roleCheck != nil && !roleCheck(act.ApproverRole) {
		return nil, ErrForbiddenRole
	}
	now := time.Now()

	if decision == DecisionReject {
		if _, err := s.orm.ApprovalAction.UpdateOneID(act.ID).
			SetStatus(entact.StatusRejected).
			SetActedBy(userID).
			SetActedAt(now).
			SetComment(comment).
			Save(ctx); err != nil {
			return nil, err
		}
		return s.orm.ApprovalRequest.UpdateOneID(req.ID).
			SetStatus(entreq.StatusRejected).
			SetDecidedAt(now).
			Save(ctx)
	}

	// Approve the current step.
	if _, err := s.orm.ApprovalAction.UpdateOneID(act.ID).
		SetStatus(entact.StatusApproved).
		SetActedBy(userID).
		SetActedAt(now).
		SetComment(comment).
		Save(ctx); err != nil {
		return nil, err
	}
	// Advance to the next pending step, if any.
	next, err := s.orm.ApprovalAction.Query().
		Where(
			entact.RequestID(req.ID),
			entact.StatusEQ(entact.StatusPending),
			entact.SequenceGT(act.Sequence),
		).
		Order(ent.Asc(entact.FieldSequence)).
		First(ctx)
	if err == nil && next != nil {
		return s.orm.ApprovalRequest.UpdateOneID(req.ID).
			SetCurrentSequence(next.Sequence).
			Save(ctx)
	}
	// No more steps → fully approved.
	return s.orm.ApprovalRequest.UpdateOneID(req.ID).
		SetStatus(entreq.StatusApproved).
		SetDecidedAt(now).
		Save(ctx)
}

// ClaimExecution atomically marks an approved request as executed, returning true
// only for the caller that wins the claim. It is a compare-and-set on executed_at:
// the UPDATE matches only rows whose executed_at is still null, so exactly one
// caller ever sees ok=true even if the approval-execution path and a legacy client
// retry race. Callers apply the effecting action (post the stock) only when ok.
func (s *Service) ClaimExecution(ctx context.Context, tenantID, requestID uuid.UUID) (bool, error) {
	n, err := s.orm.ApprovalRequest.Update().
		Where(
			entreq.ID(requestID),
			entreq.TenantID(tenantID),
			entreq.ExecutedAtIsNil(),
		).
		SetExecutedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ReleaseExecution clears the execution claim so a failed effecting action can be
// retried by a later approval-execution pass (the approval itself stays approved).
func (s *Service) ReleaseExecution(ctx context.Context, tenantID, requestID uuid.UUID) error {
	_, err := s.orm.ApprovalRequest.Update().
		Where(entreq.ID(requestID), entreq.TenantID(tenantID)).
		ClearExecutedAt().
		Save(ctx)
	return err
}

// Latest returns the most recent approval request for an object (nil if none).
func (s *Service) Latest(ctx context.Context, tenantID, objectID uuid.UUID) (*ent.ApprovalRequest, error) {
	req, err := s.orm.ApprovalRequest.Query().
		Where(entreq.TenantID(tenantID), entreq.ObjectID(objectID)).
		Order(ent.Desc(entreq.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return req, err
}

// Satisfied reports whether a document with the given amount may proceed past its
// approval gate: true when no rule matches (no approval needed) or the latest
// request is approved. The returned state is one of: "not_required", "approved",
// "pending", "rejected", "not_submitted".
func (s *Service) Satisfied(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, amount float64) (bool, string, error) {
	rule, err := s.MatchRule(ctx, tenantID, module, amount)
	if err != nil {
		return false, "", err
	}
	if rule == nil || len(rule.Edges.Steps) == 0 {
		return true, "not_required", nil
	}
	req, err := s.Latest(ctx, tenantID, objectID)
	if err != nil {
		return false, "", err
	}
	if req == nil {
		return false, "not_submitted", nil
	}
	switch req.Status {
	case entreq.StatusApproved:
		return true, "approved", nil
	case entreq.StatusPending:
		return false, "pending", nil
	case entreq.StatusRejected:
		return false, "rejected", nil
	default:
		return false, string(req.Status), nil
	}
}
