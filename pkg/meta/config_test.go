/*
 * JuiceFS, Copyright 2021 Juicedata, Inc.
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

package meta

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/juicedata/juicefs/pkg/object"
	"github.com/juicedata/juicefs/pkg/version"
	"github.com/stretchr/testify/assert"
)

func TestRemoveSecret(t *testing.T) {
	format := Format{Name: "test", SecretKey: "testSecret", EncryptKey: "testEncrypt", SessionToken: "token"}
	if err := format.Encrypt(); err != nil {
		t.Fatal(err)
	}

	format.RemoveSecret()
	if format.SecretKey != "removed" || format.EncryptKey != "removed" || format.SessionToken != "removed" {
		t.Fatalf("invalid format: %+v", format)
	}

	if err := format.Decrypt(); err != nil && !strings.Contains(err.Error(), "secret was removed") {
		t.Fatal(err)
	}
}

func TestBucketPrefixFormatCompatibility(t *testing.T) {
	format := Format{Name: "test", BucketPrefix: "juicefs/v1/tenant/workflow", MetaVersion: BucketPrefixMetaVersion}

	data, err := json.Marshal(format)
	if err != nil {
		t.Fatalf("marshal format: %s", err)
	}
	var decoded Format
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal format: %s", err)
	}
	if decoded.BucketPrefix != format.BucketPrefix {
		t.Fatalf("bucket prefix %q != expected %q", decoded.BucketPrefix, format.BucketPrefix)
	}
	if err = format.checkVersion(BucketPrefixMetaVersion-1, version.Parse("1.5.0-dev")); err == nil {
		t.Fatal("client without bucket-prefix metadata support was not rejected")
	}
	if err = format.checkVersion(BucketPrefixMetaVersion, version.Parse("1.5.0-dev")); err != nil {
		t.Fatalf("client with bucket-prefix metadata support was rejected: %s", err)
	}
}

func TestBucketPrefixDumpLoad(t *testing.T) {
	src := NewClient("memkv://bucket-prefix-source", nil)
	defer src.Shutdown()
	if err := src.Reset(); err != nil {
		t.Fatalf("reset source metadata: %s", err)
	}
	want := &Format{Name: "test", BucketPrefix: "juicefs/v1/tenant/workflow", MetaVersion: BucketPrefixMetaVersion}
	if err := src.Init(want, true); err != nil {
		t.Fatalf("init source metadata: %s", err)
	}

	var dump bytes.Buffer
	if err := src.DumpMetaV2(Background(), &dump, nil); err != nil {
		t.Fatalf("dump metadata: %s", err)
	}
	dst := NewClient("memkv://bucket-prefix-destination", nil)
	defer dst.Shutdown()
	if err := dst.Reset(); err != nil {
		t.Fatalf("reset destination metadata: %s", err)
	}
	if err := dst.LoadMetaV2(Background(), bytes.NewReader(dump.Bytes()), nil); err != nil {
		t.Fatalf("load metadata: %s", err)
	}
	got, err := dst.Load(false)
	if err != nil {
		t.Fatalf("load destination format: %s", err)
	}
	if got.BucketPrefix != want.BucketPrefix || got.MetaVersion != want.MetaVersion {
		t.Fatalf("dump/load format = %+v, want %+v", got, want)
	}
}

func TestFormatUpdateBucketPrefix(t *testing.T) {
	old := &Format{Name: "test", BucketPrefix: "one"}
	if err := (&Format{Name: "test", BucketPrefix: "two"}).update(old, false); err == nil {
		t.Fatal("changing bucket prefix should fail")
	}
	if err := (&Format{Name: "test"}).update(old, false); err == nil {
		t.Fatal("removing bucket prefix should fail")
	}
}

func TestEncrypt(t *testing.T) {
	cases := []struct {
		algo string
	}{
		{object.AES256GCM_RSA},
		{object.CHACHA20_RSA},
		{object.SM4GCM},
	}
	format := Format{Name: "test", SecretKey: "testSecret", SessionToken: "token", EncryptKey: "testEncrypt"}
	for _, c := range cases {
		format.EncryptAlgo = c.algo
		t.Run(c.algo, func(t *testing.T) {
			if err := format.Encrypt(); err != nil {
				t.Fatalf("Format encrypt: %s", err)
			}
			if format.SecretKey == "testSecret" || format.SessionToken == "token" || format.EncryptKey == "testEncrypt" {
				t.Fatalf("invalid format: %+v", format)
			}
			if err := format.Decrypt(); err != nil {
				t.Fatalf("Format decrypt: %s", err)
			}
			if format.SecretKey != "testSecret" || format.SessionToken != "token" || format.EncryptKey != "testEncrypt" {
				t.Fatalf("invalid format: %+v", format)
			}
		})
	}
}

func TestFormat_Update_KeyConflict(t *testing.T) {
	oldFormat := Format{Name: "test", UUID: "UUID-A"}

	newFormat := Format{Name: "test", UUID: "UUID-B", SecretKey: "secret"}
	if err := newFormat.Encrypt(); err != nil {
		t.Fatal(err)
	}
	assert.True(t, newFormat.KeyEncrypted)

	if err := newFormat.update(&oldFormat, false); err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, "UUID-A", newFormat.UUID)
	assert.True(t, newFormat.KeyEncrypted)

	if err := newFormat.Decrypt(); err != nil {
		t.Fatalf("failed to decrypt with new UUID (which is old UUID A): %s", err)
	}

	assert.Equal(t, "secret", newFormat.SecretKey)
}
