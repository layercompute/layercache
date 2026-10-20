package server

import (
	"context"
	"errors"
	"strings"

	"github.com/layercache/layercache/internal/access"
)

var ErrCapacityDenied = errors.New("cache capacity permission denied")

// ProjectCapacity keeps the operating quota distinct from the allocation that
// the operator or storage plan reserves for this project.
type ProjectCapacity struct {
	State                string `json:"state"`
	MaxBytes             *int64 `json:"maxBytes"`
	UsedBytes            *int64 `json:"usedBytes"`
	ReservedBytes        *int64 `json:"reservedBytes"`
	RetainedBytes        *int64 `json:"retainedBytes"`
	RetainedStagingBytes *int64 `json:"retainedStagingBytes"`
	SoftTargetBytes      *int64 `json:"softTargetBytes"`
	MinimumBytes         *int64 `json:"minimumBytes"`
	CeilingBytes         *int64 `json:"ceilingBytes"`
	CanChange            bool   `json:"canChange"`
	LimitSource          string `json:"limitSource"`
	Reason               string `json:"reason,omitempty"`
}

// Capacity is an in-process dashboard operation. It does not expose an
// administrator secret or change native managed-quota authorization.
func (gateway *ProjectGateway) Capacity(ctx context.Context, project, subject string) (ProjectCapacity, error) {
	gateway.mu.RLock()
	defer gateway.mu.RUnlock()
	instance, ok := gateway.projects[project]
	if gateway.closed || !ok || !instance.dashboardCapacitySubject(subject) {
		return ProjectCapacity{}, ErrCapacityDenied
	}
	claims := access.Claims{Subject: subject, Project: project}
	if !instance.currentMembershipAllows(ctx, claims, access.CapabilityRead) {
		return ProjectCapacity{}, ErrCapacityDenied
	}
	return instance.capacity(ctx, instance.currentMembershipAllows(ctx, claims, access.CapabilityAdmin))
}

func (gateway *ProjectGateway) SetCapacity(ctx context.Context, project, subject string, expected, maximum int64) (ProjectCapacity, error) {
	gateway.mu.RLock()
	defer gateway.mu.RUnlock()
	instance, ok := gateway.projects[project]
	if gateway.closed || !ok || !instance.dashboardCapacitySubject(subject) || !instance.currentMembershipAllows(ctx, access.Claims{Subject: subject, Project: project}, access.CapabilityAdmin) {
		return ProjectCapacity{}, ErrCapacityDenied
	}
	if instance.cloudStore == nil {
		return ProjectCapacity{}, errors.New("capacity changes require hosted project storage")
	}
	if maximum > instance.config.MaxBytes {
		return ProjectCapacity{}, ErrCapacityDenied
	}
	if _, err := instance.cloudStore.SetCapacity(ctx, subject, expected, maximum, instance.config.MaxBytes); err != nil {
		return ProjectCapacity{}, err
	}
	return instance.capacity(ctx, true)
}

func (instance *Server) dashboardCapacitySubject(subject string) bool {
	prefix := "github:"
	if instance.projectAuthority != nil {
		prefix = "github-id:"
	}
	return strings.HasPrefix(subject, prefix) && len(subject) > len(prefix)
}

func (instance *Server) capacity(ctx context.Context, administrator bool) (ProjectCapacity, error) {
	result := ProjectCapacity{State: "unsupported", LimitSource: "operator", Reason: "Capacity changes require hosted project storage."}
	if instance.projectAuthority != nil {
		result.LimitSource = "plan"
	}
	if instance.cloudStore == nil {
		return result, nil
	}
	quota, err := instance.cloudStore.Quota(ctx)
	if err != nil {
		return ProjectCapacity{}, err
	}
	minimum := max(int64(1), quota.UsedBytes+quota.ReservedBytes, quota.RetainedStagingBytes, instance.config.CacheSoftBytes)
	result.State, result.Reason = "connected", ""
	result.MaxBytes, result.UsedBytes, result.ReservedBytes = &quota.MaxBytes, &quota.UsedBytes, &quota.ReservedBytes
	result.RetainedBytes, result.RetainedStagingBytes = &quota.RetainedBytes, &quota.RetainedStagingBytes
	result.SoftTargetBytes, result.MinimumBytes, result.CeilingBytes = &instance.config.CacheSoftBytes, &minimum, &instance.config.MaxBytes
	result.CanChange = administrator && instance.config.MaxBytes <= 1<<53-1 && minimum <= instance.config.MaxBytes
	if !administrator {
		result.Reason = "An administrator can change capacity within the assigned allocation."
	} else if !result.CanChange {
		result.Reason = "Current obligations require an operator allocation review."
	}
	return result, nil
}
