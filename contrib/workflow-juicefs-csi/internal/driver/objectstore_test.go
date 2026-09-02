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
	"testing"
)

type fakeObjectStore struct {
	data     map[string][]byte
	etags    map[string]string
	getError error
	putCount int
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{data: make(map[string][]byte), etags: make(map[string]string)}
}

func (s *fakeObjectStore) get(_ context.Context, key string) ([]byte, string, error) {
	if s.getError != nil {
		err := s.getError
		s.getError = nil
		return nil, "", err
	}
	data, ok := s.data[key]
	if !ok {
		return nil, "", errObjectNotFound
	}
	return append([]byte(nil), data...), s.etags[key], nil
}

func (s *fakeObjectStore) putIfAbsent(_ context.Context, key string, data []byte) (string, error) {
	if _, ok := s.data[key]; ok {
		return "", errObjectPrecondition
	}
	s.putCount++
	etag := fmt.Sprintf("etag-%d", s.putCount)
	s.data[key], s.etags[key] = append([]byte(nil), data...), etag
	return etag, nil
}

func (s *fakeObjectStore) putIfMatch(_ context.Context, key, etag string, data []byte) (string, error) {
	if s.etags[key] != etag {
		return "", errObjectPrecondition
	}
	s.putCount++
	newETag := fmt.Sprintf("etag-%d", s.putCount)
	s.data[key], s.etags[key] = append([]byte(nil), data...), newETag
	return newETag, nil
}

func TestReadOrInitializePointerUsesConditionalExactOperations(t *testing.T) {
	store := newFakeObjectStore()
	pointer, etag, err := readOrInitializePointer(context.Background(), store, "workspace/metadata/latest.json")
	if err != nil || pointer.Version != 1 || pointer.Generation != "" || etag == "" {
		t.Fatalf("initialize pointer = %+v, %q, %v", pointer, etag, err)
	}
	store.getError = errObjectAccessDenied
	pointer, _, err = readOrInitializePointer(context.Background(), store, "workspace/metadata/latest.json")
	if err != nil || pointer.Version != 1 {
		t.Fatalf("ambiguous get with lost initialization race = %+v, %v", pointer, err)
	}
}

func TestDecodePointerRejectsUnknownAndUnsafeGeneration(t *testing.T) {
	for _, data := range []string{
		`{"version":2}`,
		`{"version":1,"generation":"../other.json.gz"}`,
		`{"version":1,"unknown":true}`,
	} {
		if _, err := decodePointer([]byte(data)); err == nil {
			t.Errorf("decodePointer(%s) succeeded", data)
		}
	}
	if !errors.Is(errObjectPrecondition, errObjectPrecondition) {
		t.Fatal("sentinel error unexpectedly changed")
	}
}
