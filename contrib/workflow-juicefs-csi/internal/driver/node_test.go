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
	"path/filepath"
	"testing"
)

func TestParseTarget(t *testing.T) {
	root := "/var/lib/kubelet"
	valid := filepath.Join(root, "pods", "pod-uid", "volumes", "kubernetes.io~csi", "volume", "mount")
	podUID, volume, err := parseTarget(root, valid)
	if err != nil || podUID != "pod-uid" || volume != "volume" {
		t.Fatalf("parseTarget(valid) = %q, %q, %v", podUID, volume, err)
	}
	for _, target := range []string{
		"relative/path",
		root + "/pods/pod-uid/volumes/kubernetes.io~csi/volume/mount/child",
		root + "/pods/pod-uid/volumes/other/volume/mount",
		root + "/../outside",
	} {
		if _, _, err = parseTarget(root, target); err == nil {
			t.Errorf("parseTarget(%q) succeeded", target)
		}
	}
}

func TestValidateRequestUsesOnlyKubeletPodIdentity(t *testing.T) {
	config := Config{
		KubeletRoot: "/var/lib/kubelet", ObjectPrefix: "juicefs-csi-poc",
		AllowedNamespace: "ai-workflows", AllowedServiceAccount: "ai-workflows",
	}
	service := newService(config, nil)
	target := "/var/lib/kubelet/pods/uid-1/volumes/kubernetes.io~csi/volume/mount"
	attributes := map[string]string{
		ephemeralKey: "true", podNameKey: "proof", podNamespaceKey: "ai-workflows",
		podUIDKey: "uid-1", serviceAccountKey: "ai-workflows",
	}
	pod, err := service.validateRequest(target, attributes)
	if err != nil {
		t.Fatalf("validateRequest: %v", err)
	}
	if pod.objectDataPrefix != "juicefs-csi-poc/architecture/ai-workflows/uid-1" {
		t.Fatalf("object prefix = %q", pod.objectDataPrefix)
	}

	attributes[podUIDKey] = "another-pod"
	if _, err = service.validateRequest(target, attributes); err == nil {
		t.Fatal("mismatched pod UID was accepted")
	}
	attributes[podUIDKey] = "uid-1"
	attributes[serviceAccountKey] = "another-service-account"
	if _, err = service.validateRequest(target, attributes); err == nil {
		t.Fatal("unexpected service account was accepted")
	}
}

func TestValidateRelativePrefix(t *testing.T) {
	for _, valid := range []string{"one", "one/two", "juicefs/v1/tenant-1/workflow_uid"} {
		if err := validateRelativePrefix(valid); err != nil {
			t.Errorf("validateRelativePrefix(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "/absolute", "one//two", "one/../two", "one?query", "one\\two", "one/space here"} {
		if err := validateRelativePrefix(invalid); err == nil {
			t.Errorf("validateRelativePrefix(%q) succeeded", invalid)
		}
	}
}
