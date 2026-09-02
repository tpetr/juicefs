/*
 * Copyright 2026 Semgrep, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	errLeaseHeld      = errors.New("workspace lease is held")
	errAmbiguousLease = errors.New("workspace has an expired lease in an ambiguous phase")
)

const leasePhaseAnnotation = "workspaces.semgrep.dev/phase"

type leaseHandle struct {
	kube      kubernetes.Interface
	namespace string
	name      string
	holder    string
	duration  time.Duration
	interval  time.Duration
	logger    *slog.Logger
	cancel    context.CancelFunc
	lost      atomic.Bool
}

func acquireLease(ctx context.Context, kube kubernetes.Interface, config Config, workspaceHash, holder string, logger *slog.Logger) (*leaseHandle, error) {
	name := "workspace-" + workspaceHash
	durationSeconds := int32(config.LeaseDuration / time.Second)
	now := metav1.NewMicroTime(time.Now().UTC())
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: config.LeaseNamespace, Labels: map[string]string{"workspaces.semgrep.dev/workspace-hash": workspaceHash}, Annotations: map[string]string{leasePhaseAnnotation: "preparing"}},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &durationSeconds, AcquireTime: &now, RenewTime: &now},
	}
	leases := kube.CoordinationV1().Leases(config.LeaseNamespace)
	created, err := leases.Create(ctx, lease, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := leases.Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return nil, fmt.Errorf("inspect existing lease: %w", getErr)
		}
		if existing.Spec.HolderIdentity != nil && *existing.Spec.HolderIdentity == holder {
			created = existing
		} else if leaseExpired(existing, time.Now()) {
			phase := existing.Annotations[leasePhaseAnnotation]
			if phase == "mounted" || phase == "committing" || phase == "generation-uploaded" || phase == "committed" {
				return nil, fmt.Errorf("%w: phase=%s; operator must resolve possible uncommitted success", errAmbiguousLease, phase)
			}
			existing.Spec = lease.Spec
			existing.Labels = lease.Labels
			existing.Annotations = lease.Annotations
			created, err = leases.Update(ctx, existing, metav1.UpdateOptions{})
		} else {
			return nil, fmt.Errorf("%w: retry after the current publisher commits or discards", errLeaseHeld)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("acquire Kubernetes Lease: %w", err)
	}
	_ = created
	handle := &leaseHandle{kube: kube, namespace: config.LeaseNamespace, name: name, holder: holder, duration: config.LeaseDuration, interval: config.LeaseRenewInterval, logger: logger}
	renewContext, cancel := context.WithCancel(context.Background())
	handle.cancel = cancel
	go handle.renewLoop(renewContext)
	return handle, nil
}

func leaseExpired(lease *coordinationv1.Lease, now time.Time) bool {
	if lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil {
		return true
	}
	return lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second).Before(now)
}

func (h *leaseHandle) renewLoop(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.renew(ctx, ""); err != nil {
				h.lost.Store(true)
				h.logger.Error("workspace lease renewal failed", "lease", h.name, "error", err)
				return
			}
		}
	}
}

func (h *leaseHandle) renew(ctx context.Context, phase string) error {
	leases := h.kube.CoordinationV1().Leases(h.namespace)
	lease, err := leases.Get(ctx, h.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != h.holder {
		return fmt.Errorf("lease holder changed")
	}
	now := metav1.NewMicroTime(time.Now().UTC())
	lease.Spec.RenewTime = &now
	if phase != "" {
		if lease.Annotations == nil {
			lease.Annotations = make(map[string]string)
		}
		lease.Annotations[leasePhaseAnnotation] = phase
	}
	_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
	return err
}

func (h *leaseHandle) setPhase(ctx context.Context, phase string) error {
	if h.lost.Load() {
		return fmt.Errorf("workspace lease was lost")
	}
	return h.renew(ctx, phase)
}

func (h *leaseHandle) release(ctx context.Context) error {
	h.cancel()
	leases := h.kube.CoordinationV1().Leases(h.namespace)
	lease, err := leases.Get(ctx, h.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != h.holder {
		return fmt.Errorf("refusing to release a lease owned by another publisher")
	}
	uid := lease.UID
	rv := lease.ResourceVersion
	return leases.Delete(ctx, h.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
}
