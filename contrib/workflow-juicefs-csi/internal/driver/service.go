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
	"fmt"
	"log/slog"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type service struct {
	csipb.UnimplementedIdentityServer
	csipb.UnimplementedNodeServer
	config   Config
	logger   *slog.Logger
	kube     kubernetes.Interface
	objects  objectStore
	sessions map[string]*mountSession
	locks    map[string]struct{}
	mu       sync.Mutex
}

func newService(ctx context.Context, config Config, logger *slog.Logger) (*service, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("load in-cluster Kubernetes configuration: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return &service{
		config: config, logger: logger, kube: kube, objects: newS3ObjectStore(s3.NewFromConfig(awsConfig), config.Bucket),
		sessions: make(map[string]*mountSession), locks: make(map[string]struct{}),
	}, nil
}

func (s *service) GetPluginInfo(context.Context, *csipb.GetPluginInfoRequest) (*csipb.GetPluginInfoResponse, error) {
	return &csipb.GetPluginInfoResponse{Name: driverName, VendorVersion: driverVersion}, nil
}

func (s *service) GetPluginCapabilities(context.Context, *csipb.GetPluginCapabilitiesRequest) (*csipb.GetPluginCapabilitiesResponse, error) {
	return &csipb.GetPluginCapabilitiesResponse{}, nil
}

func (s *service) Probe(context.Context, *csipb.ProbeRequest) (*csipb.ProbeResponse, error) {
	return &csipb.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}

func (s *service) NodeGetInfo(context.Context, *csipb.NodeGetInfoRequest) (*csipb.NodeGetInfoResponse, error) {
	return &csipb.NodeGetInfoResponse{NodeId: s.config.NodeID}, nil
}

func (s *service) NodeGetCapabilities(context.Context, *csipb.NodeGetCapabilitiesRequest) (*csipb.NodeGetCapabilitiesResponse, error) {
	return &csipb.NodeGetCapabilitiesResponse{}, nil
}

func (s *service) acquire(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.locks[target]; exists {
		return false
	}
	s.locks[target] = struct{}{}
	return true
}

func (s *service) release(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.locks, target)
}

func (s *service) session(volumeID string) *mountSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[volumeID]
}

func (s *service) setSession(volumeID string, session *mountSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session == nil {
		delete(s.sessions, volumeID)
	} else {
		s.sessions[volumeID] = session
	}
}

func validateCapability(capability *csipb.VolumeCapability) error {
	if capability == nil || capability.GetMount() == nil || capability.GetAccessMode() == nil {
		return status.Error(codes.InvalidArgument, "a mount volume capability and access mode are required")
	}
	if capability.GetAccessMode().GetMode() != csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
		return status.Error(codes.InvalidArgument, "only SINGLE_NODE_WRITER is supported by this POC")
	}
	return nil
}
