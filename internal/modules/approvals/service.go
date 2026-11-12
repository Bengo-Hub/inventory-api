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
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entact "github.com/bengobox/inventory-service/internal/ent/approvalaction"
	entreq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entrule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
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
	req, required, _, err := s.SubmitOnce(ctx, tenantID, module, objectID, objectRef, amount, submittedBy, payload)
	return req, required, err
}

// OpenRequest returns the live request for (tenant, module, object): the pending one, or an
// approved one whose effect has not been executed yet. Nil when there is none.
func (s *Service) OpenRequest(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID) (*ent.ApprovalRequest, error) {
	req, err := s.orm.ApprovalRequest.Query().
		Where(
			entreq.TenantID(tenantID),
			entreq.ModuleEQ(entreq.Module(module)),
			entreq.ObjectID(objectID),
			entreq.Or(
				entreq.StatusEQ(entreq.StatusPending),
				entreq.And(entreq.StatusEQ(entreq.StatusApproved), entreq.ExecutedAtIsNil()),
			),
		).
		Order(ent.Desc(entreq.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return req, err
}

// SubmitOnce is SubmitWithPayload that never opens a second live workflow for a document. When
// a pending request (or an approved one not yet executed) already exists for the same tenant,
// module and object at the same amount, it is returned with reused=true and nothing is written.
// A live request (pending, or approved and not executed) at a different amount belongs to a
// different document state, so it is cancelled and a fresh request is created; an old approval
// can then never come back into force. Executed requests are history and are never touched.
// The partial unique index approvalrequest_pending_unique backs this up: if a racing submit
// wins the insert, the constraint error is answered with the racer's request (reused is true
// only when the racer is for the same amount).
func (s *Service) SubmitOnce(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, objectRef string, amount float64, submittedBy *uuid.UUID, payload map[string]any) (*ent.ApprovalRequest, bool, bool, error) {
	rule, err := s.MatchRule(ctx, tenantID, module, amount)
	if err != nil {
		return nil, false, false, err
	}
	if rule == nil {
		return nil, false, false, nil
	}
	steps := append([]*ent.ApprovalStep(nil), rule.Edges.Steps...)
	if len(steps) == 0 {
		return nil, false, false, nil // a rule with no steps imposes no gate
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Sequence < steps[j].Sequence })

	open, err := s.OpenRequest(ctx, tenantID, module, objectID)
	if err != nil {
		return nil, false, false, err
	}
	if open != nil && open.Amount == amount {
		return open, true, true, nil
	}

	req, err := s.createRequest(ctx, tenantID, module, objectID, objectRef, amount, submittedBy, payload, rule.ID, steps)
	if err != nil && ent.IsConstraintError(err) {
		// A concurrent submit created the pending request first; hand that one back.
		if racer, rerr := s.OpenRequest(ctx, tenantID, module, objectID); rerr == nil && racer != nil {
			return racer, true, racer.Amount == amount, nil
		}
	}
	if err != nil {
		return nil, false, false, err
	}
	return req, true, false, nil
}

// createRequest supersedes live requests at another amount and writes the new request with one
// pending action per step, all in one transaction.
func (s *Service) createRequest(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, objectRef string, amount float64, submittedBy *uuid.UUID, payload map[string]any, ruleID uuid.UUID, steps []*ent.ApprovalStep) (*ent.ApprovalRequest, error) {
	tx, err := s.orm.Tx(ctx)
	if err != nil {
		return nil, err
	}
	req, err := createRequestTx(ctx, tx.Client(), tenantID, module, objectID, objectRef, amount, submittedBy, payload, ruleID, steps)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return req, nil
}

func createRequestTx(ctx context.Context, c *ent.Client, tenantID uuid.UUID, module string, objectID uuid.UUID, objectRef string, amount float64, submittedBy *uuid.UUID, payload map[string]any, ruleID uuid.UUID, steps []*ent.ApprovalStep) (*ent.ApprovalRequest, error) {
	// Supersede every live request for this object at a different amount (pending, or approved
	// and not executed) so there is a single live workflow and an old approval cannot return.
	// Requests at the SAME amount are left alone: SubmitOnce only gets here when none was found,
	// so one that exists now was written by a concurrent submit, and our insert must lose to it on
	// the unique index instead of cancelling it.
	if _, err := c.ApprovalRequest.Update().
		Where(
			entreq.TenantID(tenantID),
			entreq.ModuleEQ(entreq.Module(module)),
			entreq.ObjectID(objectID),
			entreq.AmountNEQ(amount),
			entreq.Or(
				entreq.StatusEQ(entreq.StatusPending),
				entreq.And(entreq.StatusEQ(entreq.StatusApproved), entreq.ExecutedAtIsNil()),
			),
		).
		SetStatus(entreq.StatusCancelled).
		SetDecidedAt(time.Now()).
		Save(ctx); err != nil {
		return nil, err
	}

	rc := c.ApprovalRequest.Create().
		SetTenantID(tenantID).
		SetModule(entreq.Module(module)).
		SetObjectID(objectID).
		SetObjectReference(objectRef).
		SetAmount(amount).
		SetRuleID(ruleID).
		SetCurrentSequence(steps[0].Sequence)
	if submittedBy != nil {
		rc = rc.SetSubmittedBy(*submittedBy)
	}
	if len(payload) > 0 {
		rc = rc.SetPayload(payload)
	}
	req, err := rc.Save(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range steps {
		if _, err := c.ApprovalAction.Create().
			SetTenantID(tenantID).
			SetRequestID(req.ID).
			SetSequence(st.Sequence).
			SetName(st.Name).
			SetApproverRole(st.ApproverRole).
			Save(ctx); err != nil {
			return nil, err
		}
	}
	return req, nil
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
			// Only an approved request can be executed: one a platform owner cancelled in the
			// meantime must never run.
			entreq.StatusEQ(entreq.StatusApproved),
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

// Gate and Check states. A gated action proceeds on StateNotRequired and StateApproved; every
// other state means it waits for sign-off.
const (
	StateNotRequired  = "not_required"  // no rule with steps matches: no approval needed
	StateApproved     = "approved"      // an approved request for this exact submission awaits execution
	StatePending      = "pending"       // a request for this exact submission is waiting for approvers
	StateRejected     = "rejected"      // the latest request was rejected (Check only)
	StateNotSubmitted = "not_submitted" // nothing live for this submission (Check only)
	StateSubmitted    = "submitted"     // Gate opened the first request for the document
	StateResubmitted  = "resubmitted"   // Gate opened a new request after an earlier one (rejected, cancelled, executed or stale)
)

// GateResult is the outcome of Check or Gate. Request is the request the state refers to: the
// approved one to claim, the pending or new one, or the rejected one.
type GateResult struct {
	State   string
	Request *ent.ApprovalRequest
}

// Proceed reports whether the gated action may run now.
func (g GateResult) Proceed() bool {
	return g.State == StateNotRequired || g.State == StateApproved
}

// LatestFor returns the most recent request for (tenant, module, object), nil when there is none.
// Unlike Latest it never mixes in another module's request for the same object id.
func (s *Service) LatestFor(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID) (*ent.ApprovalRequest, error) {
	req, err := s.orm.ApprovalRequest.Query().
		Where(entreq.TenantID(tenantID), entreq.ModuleEQ(entreq.Module(module)), entreq.ObjectID(objectID)).
		Order(ent.Desc(entreq.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return req, err
}

// Check reports where a document stands at its approval gate without writing anything. A request
// counts only when it is live (pending, or approved and not executed) and was made for the same
// submission: the same amount and, when payload is given, the same payload. An approval therefore
// authorises one execution of one version of the document.
func (s *Service) Check(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, amount float64, payload map[string]any) (GateResult, error) {
	res, _, _, err := s.check(ctx, tenantID, module, objectID, amount, payload)
	return res, err
}

// check is Check plus what Gate needs to open a request: the rule's ordered steps and the stale
// live request (one made for another submission) that a new request supersedes.
func (s *Service) check(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, amount float64, payload map[string]any) (GateResult, *ent.ApprovalRule, *ent.ApprovalRequest, error) {
	rule, err := s.MatchRule(ctx, tenantID, module, amount)
	if err != nil {
		return GateResult{}, nil, nil, err
	}
	if rule == nil || len(rule.Edges.Steps) == 0 {
		return GateResult{State: StateNotRequired}, nil, nil, nil
	}
	open, err := s.OpenRequest(ctx, tenantID, module, objectID)
	if err != nil {
		return GateResult{}, nil, nil, err
	}
	if open != nil && sameSubmission(open, amount, payload) {
		if open.Status == entreq.StatusApproved {
			return GateResult{State: StateApproved, Request: open}, rule, nil, nil
		}
		return GateResult{State: StatePending, Request: open}, rule, nil, nil
	}
	latest, err := s.LatestFor(ctx, tenantID, module, objectID)
	if err != nil {
		return GateResult{}, nil, nil, err
	}
	if latest != nil && latest.Status == entreq.StatusRejected {
		return GateResult{State: StateRejected, Request: latest}, rule, open, nil
	}
	return GateResult{State: StateNotSubmitted, Request: latest}, rule, open, nil
}

// Gate is the approval gate every effecting action goes through. It returns StateNotRequired or
// StateApproved when the action may run (the caller then claims the approved request with
// ClaimExecution before acting), and StatePending while the matching request waits. In every
// other case (never submitted, rejected, cancelled, already executed, or changed since) it opens a
// new request for this submission and returns StateSubmitted or StateResubmitted. A rejected
// request stays as history, so a fixed document is resubmitted simply by pressing the action
// again. payload is stored on the new request: the inputs the approval executor needs to run the
// action on approval, and part of what identifies the submission.
func (s *Service) Gate(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID, objectRef string, amount float64, submittedBy *uuid.UUID, payload map[string]any) (GateResult, error) {
	res, rule, stale, err := s.check(ctx, tenantID, module, objectID, amount, payload)
	if err != nil {
		return res, err
	}
	switch res.State {
	case StateNotRequired, StateApproved, StatePending:
		return res, nil
	}
	steps := append([]*ent.ApprovalStep(nil), rule.Edges.Steps...)
	sort.Slice(steps, func(i, j int) bool { return steps[i].Sequence < steps[j].Sequence })
	state := StateSubmitted
	if res.Request != nil {
		state = StateResubmitted
	}

	tx, err := s.orm.Tx(ctx)
	if err != nil {
		return GateResult{}, err
	}
	if stale != nil {
		// The live request was made for another version of the document (createRequestTx only
		// supersedes other amounts; a changed payload at the same amount is closed here).
		n, err := tx.ApprovalRequest.Update().
			Where(entreq.ID(stale.ID), entreq.TenantID(tenantID), liveRequest()).
			SetStatus(entreq.StatusCancelled).SetDecidedAt(time.Now()).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return GateResult{}, err
		}
		if n == 0 {
			// The stale request changed between the check and here (most likely the executor
			// claimed it and is running its action). Opening a second request now could let the
			// action run twice, so report it as still in progress; the next press sees the
			// settled state and opens a new request if one is needed.
			_ = tx.Rollback()
			cur, gerr := s.orm.ApprovalRequest.Query().Where(entreq.ID(stale.ID), entreq.TenantID(tenantID)).Only(ctx)
			if gerr != nil {
				return GateResult{}, gerr
			}
			return GateResult{State: StatePending, Request: cur}, nil
		}
	}
	req, err := createRequestTx(ctx, tx.Client(), tenantID, module, objectID, objectRef, amount, submittedBy, payload, rule.ID, steps)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			// A concurrent press opened the request first; report that one.
			if racer, rerr := s.OpenRequest(ctx, tenantID, module, objectID); rerr == nil && racer != nil {
				if racer.Status == entreq.StatusApproved {
					return GateResult{State: StateApproved, Request: racer}, nil
				}
				return GateResult{State: StatePending, Request: racer}, nil
			}
		}
		return GateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return GateResult{}, err
	}
	return GateResult{State: state, Request: req}, nil
}

// WithdrawLive cancels the document's live requests (pending, or approved and not executed) for
// the module, because the document changed under them. Executed requests are history and stay.
// Returns how many requests were withdrawn.
func (s *Service) WithdrawLive(ctx context.Context, tenantID uuid.UUID, module string, objectID uuid.UUID) (int, error) {
	return s.orm.ApprovalRequest.Update().
		Where(entreq.TenantID(tenantID), entreq.ModuleEQ(entreq.Module(module)), entreq.ObjectID(objectID), liveRequest()).
		SetStatus(entreq.StatusCancelled).SetDecidedAt(time.Now()).
		Save(ctx)
}

// liveRequest matches a request that still governs its document: pending, or approved and not
// executed yet.
func liveRequest() predicate.ApprovalRequest {
	return entreq.Or(
		entreq.StatusEQ(entreq.StatusPending),
		entreq.And(entreq.StatusEQ(entreq.StatusApproved), entreq.ExecutedAtIsNil()),
	)
}

// sameSubmission reports whether a live request was made for this version of the document: the
// same amount and, when the caller passes a payload, an equal payload once both are read as JSON.
func sameSubmission(req *ent.ApprovalRequest, amount float64, payload map[string]any) bool {
	if req.Amount != amount {
		return false
	}
	if len(payload) == 0 {
		return true
	}
	return reflect.DeepEqual(asJSON(req.Payload), asJSON(payload))
}

// asJSON reads a payload the way it comes back from the database, so a map built from typed
// values compares equal to the stored copy.
func asJSON(p map[string]any) any {
	if len(p) == 0 {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
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
