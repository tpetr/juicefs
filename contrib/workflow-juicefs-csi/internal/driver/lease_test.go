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
	"io"
	"log/slog"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLeaseExcludesWriterAndFailsClosedAfterMountedExpiry(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewSimpleClientset()
	config := Config{LeaseNamespace: "ai-workflows", LeaseDuration: 90 * time.Second, LeaseRenewInterval: time.Hour}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := acquireLease(ctx, kube, config, "workspacehash", "holder-a", logger)
	if err != nil {
		t.Fatal(err)
	}
	first.cancel()
	if _, err = acquireLease(ctx, kube, config, "workspacehash", "holder-b", logger); !errors.Is(err, errLeaseHeld) {
		t.Fatalf("second acquire error = %v, want lease held", err)
	}
	if err = first.setPhase(ctx, "mounted"); err != nil {
		t.Fatal(err)
	}
	lease, err := kube.CoordinationV1().Leases(config.LeaseNamespace).Get(ctx, first.name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	old := metav1.NewMicroTime(time.Now().Add(-10 * time.Minute))
	lease.Spec.RenewTime = &old
	if _, err = kube.CoordinationV1().Leases(config.LeaseNamespace).Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = acquireLease(ctx, kube, config, "workspacehash", "holder-b", logger); !errors.Is(err, errAmbiguousLease) {
		t.Fatalf("expired mounted acquire error = %v, want ambiguous", err)
	}
	if err = first.release(ctx); err != nil {
		t.Fatal(err)
	}
}
