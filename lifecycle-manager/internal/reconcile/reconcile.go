// Package reconcile computes the claims↔instances diff surfaced in /status.
// Policy is report-only (HLM_ORPHAN_POLICY=report): auto-deleting connector
// state that might belong to non-orchestrator gateways is the wrong default.
package reconcile

import (
	"sync"
	"time"

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/session"
)

// Report is the last reconcile result.
type Report struct {
	// ClaimsWithoutInstances: connector-flagged sessions with no connector
	// row. Usually transient (agent hasn't self-provisioned yet); persistent
	// entries mean the connector lost state.
	ClaimsWithoutInstances []string `json:"claimsWithoutInstances"`
	// InstancesWithoutClaims: connector rows with no managed claim. May be
	// legitimately unmanaged gateways — report only, never delete.
	InstancesWithoutClaims []string `json:"instancesWithoutClaims"`
	LastRun                int64    `json:"lastRun"` // unix seconds
}

// Store holds the latest report.
type Store struct {
	mu     sync.RWMutex
	report *Report
}

// Update recomputes the report. instances == nil (connector unreachable or
// throttled) skips the update rather than reporting garbage.
func (s *Store) Update(claims []k8s.Claim, instances map[string]connector.Instance) {
	if instances == nil {
		return
	}
	report := &Report{
		ClaimsWithoutInstances: []string{},
		InstancesWithoutClaims: []string{},
		LastRun:                time.Now().Unix(),
	}
	claimed := map[string]bool{}
	for i := range claims {
		claim := &claims[i]
		claimed[claim.Name] = true
		if claim.Annotations[session.AnnoConnector] == "true" {
			if _, ok := instances[claim.Name]; !ok {
				report.ClaimsWithoutInstances = append(report.ClaimsWithoutInstances, claim.Name)
			}
		}
	}
	for id := range instances {
		if !claimed[id] {
			report.InstancesWithoutClaims = append(report.InstancesWithoutClaims, id)
		}
	}
	s.mu.Lock()
	s.report = report
	s.mu.Unlock()
}

// Load returns the latest report, if one has been computed.
func (s *Store) Load() (*Report, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.report, s.report != nil
}
