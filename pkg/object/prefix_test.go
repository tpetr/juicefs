/*
 * JuiceFS, Copyright 2026 Juicedata, Inc.
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

package object

import (
	"context"
	"testing"
)

type multipartRecordingStorage struct {
	ObjectStorage
	keys []string
}

func (s *multipartRecordingStorage) CreateMultipartUpload(_ context.Context, key string) (*MultipartUpload, error) {
	s.keys = append(s.keys, "create:"+key)
	return &MultipartUpload{UploadID: "upload"}, nil
}

func (s *multipartRecordingStorage) UploadPart(_ context.Context, key, _ string, _ int, _ []byte) (*Part, error) {
	s.keys = append(s.keys, "upload:"+key)
	return &Part{}, nil
}

func (s *multipartRecordingStorage) AbortUpload(_ context.Context, key, _ string) {
	s.keys = append(s.keys, "abort:"+key)
}

func (s *multipartRecordingStorage) CompleteUpload(_ context.Context, key, _ string, _ []*Part) error {
	s.keys = append(s.keys, "complete:"+key)
	return nil
}

func (s *multipartRecordingStorage) ListUploads(_ context.Context, marker string) ([]*PendingPart, string, error) {
	s.keys = append(s.keys, "list:"+marker)
	return []*PendingPart{{Key: "prefix/existing"}}, "prefix/next", nil
}

func TestWithPrefixScopesMultipartOperations(t *testing.T) {
	baseStorage, err := CreateStorage("mem", "multipart-prefix-test", "", "", "")
	if err != nil {
		t.Fatalf("create base storage: %s", err)
	}
	base := &multipartRecordingStorage{ObjectStorage: baseStorage}
	storage := WithPrefix(base, "prefix/")
	ctx := context.Background()

	upload, err := storage.CreateMultipartUpload(ctx, "key")
	if err != nil {
		t.Fatalf("create multipart upload: %s", err)
	}
	if _, err = storage.UploadPart(ctx, "key", upload.UploadID, 1, []byte("data")); err != nil {
		t.Fatalf("upload part: %s", err)
	}
	if err = storage.CompleteUpload(ctx, "key", upload.UploadID, nil); err != nil {
		t.Fatalf("complete upload: %s", err)
	}
	storage.AbortUpload(ctx, "key", upload.UploadID)
	parts, marker, err := storage.ListUploads(ctx, "marker")
	if err != nil {
		t.Fatalf("list uploads: %s", err)
	}
	want := []string{"create:prefix/key", "upload:prefix/key", "complete:prefix/key", "abort:prefix/key", "list:prefix/marker"}
	if len(base.keys) != len(want) {
		t.Fatalf("multipart keys = %v, want %v", base.keys, want)
	}
	for i := range want {
		if base.keys[i] != want[i] {
			t.Fatalf("multipart keys = %v, want %v", base.keys, want)
		}
	}
	if len(parts) != 1 || parts[0].Key != "existing" || marker != "next" {
		t.Fatalf("list uploads = %+v, %q", parts, marker)
	}
}

func TestDirStorage(t *testing.T) {
	base, _ := CreateStorage("mem", "bucket", "", "", "")

	tests := []struct {
		name    string
		storage func() ObjectStorage
		wantStr string
	}{
		{
			name:    "withPrefix_dir",
			storage: func() ObjectStorage { return WithPrefix(base, "subdir/") },
			wantStr: base.String() + "subdir/",
		},
		{
			name:    "withPrefix_file",
			storage: func() ObjectStorage { return WithPrefix(base, "subdir/file") },
			wantStr: base.String() + "subdir/",
		},
		{
			name:    "withPrefix_toplevel_file",
			storage: func() ObjectStorage { return WithPrefix(base, "file") },
			wantStr: base.String(),
		},
		{
			name:    "withPrefix_empty",
			storage: func() ObjectStorage { return WithPrefix(base, "") },
			wantStr: base.String(),
		},
		{
			name:    "withPrefix_nested_file",
			storage: func() ObjectStorage { return WithPrefix(base, "a/b/c") },
			wantStr: base.String() + "a/b/",
		},
		{
			name: "filestore_dir",
			storage: func() ObjectStorage {
				fs, _ := CreateStorage("file", "/tmp/", "", "", "")
				return fs
			},
			wantStr: "file:///tmp/",
		},
		{
			name: "filestore_file",
			storage: func() ObjectStorage {
				return &filestore{root: "/tmp/target"}
			},
			wantStr: "file:///tmp/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.storage()
			got := DirStorage(s)
			if got.String() != tc.wantStr {
				t.Errorf("DirStorage(%q).String() = %q, want %q", s.String(), got.String(), tc.wantStr)
			}
		})
	}
}
