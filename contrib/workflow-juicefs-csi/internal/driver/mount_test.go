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
	"strings"
	"testing"
)

func TestTailBufferKeepsBoundedSuffix(t *testing.T) {
	buffer := newTailBuffer(8)
	if _, err := buffer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("-second")); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "t-second" {
		t.Fatalf("tail = %q, want %q", got, "t-second")
	}
}

func TestEscapeMountInfoPath(t *testing.T) {
	got := escapeMountInfoPath("/path with\\special\tname")
	for _, want := range []string{`\040`, `\134`, `\011`} {
		if !strings.Contains(got, want) {
			t.Errorf("escaped path %q does not contain %q", got, want)
		}
	}
}
