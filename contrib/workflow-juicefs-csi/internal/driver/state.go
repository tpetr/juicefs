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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type volumeState struct {
	Version            int    `json:"version"`
	Phase              string `json:"phase"`
	VolumeID           string `json:"volumeID"`
	TargetPath         string `json:"targetPath"`
	PrivateMountPath   string `json:"privateMountPath"`
	MetadataPath       string `json:"metadataPath"`
	WorkspacePrefix    string `json:"workspacePrefix"`
	WorkspaceHash      string `json:"workspaceHash"`
	PriorGeneration    string `json:"priorGeneration,omitempty"`
	PointerETag        string `json:"pointerETag"`
	UploadedGeneration string `json:"uploadedGeneration,omitempty"`
	PodUID             string `json:"podUID"`
	LeaseName          string `json:"leaseName"`
	JuiceFSPID         int    `json:"juicefsPID,omitempty"`
	SuccessIntent      bool   `json:"successIntent"`
}

func (s *mountSession) saveState() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return saveVolumeState(s.statePath, s.state)
}

func saveVolumeState(statePath string, state volumeState) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary := filepath.Join(statePath, ".state.json.tmp")
	final := filepath.Join(statePath, "state.json")
	if err = os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write temporary volume state: %w", err)
	}
	if err = os.Rename(temporary, final); err != nil {
		return fmt.Errorf("publish volume state: %w", err)
	}
	directory, err := os.Open(statePath)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
