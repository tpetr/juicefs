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

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/juicedata/juicefs/contrib/workflow-juicefs-csi/internal/driver"
)

func main() {
	var config driver.Config
	flag.StringVar(&config.Endpoint, "endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	flag.StringVar(&config.NodeID, "node-id", "", "Kubernetes node name")
	flag.StringVar(&config.KubeletRoot, "kubelet-root", "/var/lib/kubelet", "kubelet root directory")
	flag.StringVar(&config.KubeletAccessRoot, "kubelet-access-root", "/k", "short view of the kubelet root for Unix sockets")
	flag.StringVar(&config.StateRoot, "state-root", "/var/lib/workflow-juicefs-csi", "private mount and state root")
	flag.StringVar(&config.JuiceFSPath, "juicefs-path", "/usr/local/bin/juicefs", "JuiceFS executable")
	flag.StringVar(&config.Bucket, "bucket", "", "S3 bucket name")
	flag.StringVar(&config.Region, "region", "us-west-2", "S3 region")
	flag.StringVar(&config.ObjectPrefix, "object-prefix", "juicefs-csi-poc", "S3 key prefix reserved for this driver")
	flag.StringVar(&config.Tenant, "tenant", "", "trusted tenant namespace for workspace keys")
	flag.StringVar(&config.AllowedNamespace, "allowed-namespace", "ai-workflows", "only namespace allowed to use the driver")
	flag.StringVar(&config.AllowedServiceAccount, "allowed-service-account", "ai-workflows", "only service account allowed to use the driver")
	flag.StringVar(&config.LeaseNamespace, "lease-namespace", "ai-workflows", "namespace for workspace leases")
	flag.DurationVar(&config.LeaseDuration, "lease-duration", 90*time.Second, "renewable workspace lease duration")
	flag.DurationVar(&config.LeaseRenewInterval, "lease-renew-interval", 20*time.Second, "workspace lease renewal interval")
	flag.DurationVar(&config.MountTimeout, "mount-timeout", 90*time.Second, "deadline for JuiceFS mount readiness")
	flag.DurationVar(&config.UnmountTimeout, "unmount-timeout", 90*time.Second, "deadline for clean JuiceFS unmount")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := config.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := driver.Run(ctx, config, logger); err != nil {
		logger.Error("driver stopped", "error", err)
		os.Exit(1)
	}
}
