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
	"os"
	"path/filepath"
	"strings"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	podNameKey           = "csi.storage.k8s.io/pod.name"
	podNamespaceKey      = "csi.storage.k8s.io/pod.namespace"
	podUIDKey            = "csi.storage.k8s.io/pod.uid"
	serviceAccountKey    = "csi.storage.k8s.io/serviceAccount.name"
	ephemeralKey         = "csi.storage.k8s.io/ephemeral"
	architectureTestPath = "architecture"
)

func (s *service) NodePublishVolume(ctx context.Context, req *csipb.NodePublishVolumeRequest) (*csipb.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetReadonly() {
		return nil, status.Error(codes.InvalidArgument, "read-only mounts are not supported by this POC")
	}
	if err := validateCapability(req.GetVolumeCapability()); err != nil {
		return nil, err
	}
	pod, err := s.validateRequest(req.GetTargetPath(), req.GetVolumeContext())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !s.acquire(req.GetTargetPath()) {
		return nil, status.Error(codes.Aborted, "another operation is already running for this target; kubelet may retry")
	}
	defer s.release(req.GetTargetPath())

	mounted, err := isMounted(req.GetTargetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "inspect publish target: %v", err)
	}
	if mounted {
		if session := s.session(req.GetVolumeId()); session != nil && session.targetPath == req.GetTargetPath() {
			return &csipb.NodePublishVolumeResponse{}, nil
		}
		return nil, status.Error(codes.FailedPrecondition, "target is mounted but this driver process has no matching session; inspect the node plugin after a possible restart")
	}
	if s.session(req.GetVolumeId()) != nil {
		return nil, status.Error(codes.Aborted, "volume session already exists without its target mount; inspect the node plugin")
	}

	logger := s.logger.With("operation", "NodePublishVolume", "volume_id", shortID(req.GetVolumeId()), "pod_uid", pod.uid)
	session, err := startMount(ctx, s.config, req.GetVolumeId(), req.GetTargetPath(), pod, logger)
	if err != nil {
		logger.Error("publish failed", "stage", "mount_and_bind", "retry", true, "error", err)
		return nil, status.Errorf(codes.Internal, "mount_and_bind failed for volume %s; retry=true; inspect node-plugin logs: %v", shortID(req.GetVolumeId()), err)
	}
	s.setSession(req.GetVolumeId(), session)
	logger.Info("publish complete", "stage", "ready")
	return &csipb.NodePublishVolumeResponse{}, nil
}

func (s *service) NodeUnpublishVolume(ctx context.Context, req *csipb.NodeUnpublishVolumeRequest) (*csipb.NodeUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID and target path are required")
	}
	if _, _, err := parseTarget(s.config.KubeletRoot, req.GetTargetPath()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !s.acquire(req.GetTargetPath()) {
		return nil, status.Error(codes.Aborted, "another operation is already running for this target; kubelet may retry")
	}
	defer s.release(req.GetTargetPath())

	logger := s.logger.With("operation", "NodeUnpublishVolume", "volume_id", shortID(req.GetVolumeId()))
	session := s.session(req.GetVolumeId())
	if session == nil {
		mounted, err := isMounted(req.GetTargetPath())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "inspect unpublish target: %v", err)
		}
		if mounted {
			return nil, status.Error(codes.FailedPrecondition, "target is mounted but the driver session was lost; automatic restart recovery is intentionally not implemented in Phase 1")
		}
		return &csipb.NodeUnpublishVolumeResponse{}, nil
	}
	if session.targetPath != req.GetTargetPath() {
		return nil, status.Error(codes.InvalidArgument, "target path does not match the active volume session")
	}
	if err := session.stop(ctx, s.config.UnmountTimeout); err != nil {
		logger.Error("unpublish failed", "stage", "flush_and_unmount", "retry", true, "error", err)
		return nil, status.Errorf(codes.Internal, "flush_and_unmount failed for volume %s; retry=true; inspect node-plugin logs: %v", shortID(req.GetVolumeId()), err)
	}
	s.setSession(req.GetVolumeId(), nil)
	logger.Info("unpublish complete", "stage", "clean")
	return &csipb.NodeUnpublishVolumeResponse{}, nil
}

type podIdentity struct {
	name             string
	namespace        string
	uid              string
	serviceAccount   string
	objectDataPrefix string
}

func (s *service) validateRequest(target string, attributes map[string]string) (podIdentity, error) {
	podUID, _, err := parseTarget(s.config.KubeletRoot, target)
	if err != nil {
		return podIdentity{}, err
	}
	if attributes[ephemeralKey] != "true" {
		return podIdentity{}, fmt.Errorf("%s must be true", ephemeralKey)
	}
	pod := podIdentity{
		name:           attributes[podNameKey],
		namespace:      attributes[podNamespaceKey],
		uid:            attributes[podUIDKey],
		serviceAccount: attributes[serviceAccountKey],
	}
	if pod.name == "" || pod.namespace == "" || pod.uid == "" || pod.serviceAccount == "" {
		return podIdentity{}, fmt.Errorf("trusted CSI pod metadata is incomplete")
	}
	if pod.uid != podUID {
		return podIdentity{}, fmt.Errorf("pod UID from the kubelet target does not match CSI pod metadata")
	}
	if pod.namespace != s.config.AllowedNamespace || pod.serviceAccount != s.config.AllowedServiceAccount {
		return podIdentity{}, fmt.Errorf("pod namespace or service account is not allowed")
	}
	if err := validateComponent(pod.uid); err != nil {
		return podIdentity{}, fmt.Errorf("pod UID: %w", err)
	}
	pod.objectDataPrefix = strings.Join([]string{s.config.ObjectPrefix, architectureTestPath, pod.namespace, pod.uid}, "/")
	if err := validateRelativePrefix(pod.objectDataPrefix); err != nil {
		return podIdentity{}, fmt.Errorf("derived object prefix: %w", err)
	}
	return pod, nil
}

func parseTarget(kubeletRoot, target string) (string, string, error) {
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return "", "", fmt.Errorf("target must be an absolute canonical path")
	}
	relative, err := filepath.Rel(kubeletRoot, target)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return "", "", fmt.Errorf("target is outside the configured kubelet root")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 6 || parts[0] != "pods" || parts[1] == "" || parts[2] != "volumes" || parts[3] != "kubernetes.io~csi" || parts[4] == "" || parts[5] != "mount" {
		return "", "", fmt.Errorf("target is not a kubelet CSI mount path")
	}
	return parts[1], parts[4], nil
}

func validateRelativePrefix(prefix string) error {
	if prefix == "" || strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\\?#\x00\n\r\t") {
		return fmt.Errorf("must be a non-empty relative S3 prefix")
	}
	for _, component := range strings.Split(prefix, "/") {
		if err := validateComponent(component); err != nil {
			return err
		}
	}
	return nil
}

func validateComponent(component string) error {
	if component == "" || component == "." || component == ".." || filepath.Base(component) != component || len(component) > 128 {
		return fmt.Errorf("invalid path component %q", component)
	}
	for _, r := range component {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("invalid path component %q", component)
		}
	}
	return nil
}

func ensureNewDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("state directory already exists; possible prior driver failure")
		}
		return err
	}
	return nil
}
