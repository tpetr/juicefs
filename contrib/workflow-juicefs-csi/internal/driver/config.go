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
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	driverName    = "workflow-juicefs.csi.semgrep.dev"
	driverVersion = "v0.0.1-poc"
)

type Config struct {
	Endpoint              string
	NodeID                string
	KubeletRoot           string
	StateRoot             string
	JuiceFSPath           string
	Bucket                string
	Region                string
	ObjectPrefix          string
	AllowedNamespace      string
	AllowedServiceAccount string
	MountTimeout          time.Duration
	UnmountTimeout        time.Duration
}

func (c Config) Validate() error {
	if !strings.HasPrefix(c.Endpoint, "unix://") {
		return fmt.Errorf("endpoint must use unix://")
	}
	for name, value := range map[string]string{
		"node-id": c.NodeID, "bucket": c.Bucket, "allowed-namespace": c.AllowedNamespace,
		"allowed-service-account": c.AllowedServiceAccount,
	} {
		if value == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	for name, value := range map[string]string{"kubelet-root": c.KubeletRoot, "state-root": c.StateRoot, "juicefs-path": c.JuiceFSPath} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return fmt.Errorf("%s must be an absolute canonical path", name)
		}
	}
	if err := validateRelativePrefix(c.ObjectPrefix); err != nil {
		return fmt.Errorf("object-prefix: %w", err)
	}
	if c.MountTimeout <= 0 || c.UnmountTimeout <= 0 {
		return fmt.Errorf("timeouts must be positive")
	}
	return nil
}
