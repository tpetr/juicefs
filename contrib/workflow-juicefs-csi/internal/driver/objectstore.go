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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var (
	errObjectNotFound     = errors.New("object not found")
	errObjectAccessDenied = errors.New("object access denied")
	errObjectPrecondition = errors.New("object precondition failed")
)

type objectStore interface {
	get(context.Context, string) ([]byte, string, error)
	putIfAbsent(context.Context, string, []byte) (string, error)
	putIfMatch(context.Context, string, string, []byte) (string, error)
}

type s3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type s3ObjectStore struct {
	client s3API
	bucket string
}

func newS3ObjectStore(client s3API, bucket string) objectStore {
	return &s3ObjectStore{client: client, bucket: bucket}
}

func (s *s3ObjectStore) get(ctx context.Context, key string) ([]byte, string, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, "", classifyObjectError(err)
	}
	defer output.Body.Close()
	data, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read exact object %q: %w", key, err)
	}
	return data, aws.ToString(output.ETag), nil
}

func (s *s3ObjectStore) putIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	output, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data), IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		return "", classifyObjectError(err)
	}
	return aws.ToString(output.ETag), nil
}

func (s *s3ObjectStore) putIfMatch(ctx context.Context, key, etag string, data []byte) (string, error) {
	output, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data), IfMatch: aws.String(etag),
	})
	if err != nil {
		return "", classifyObjectError(err)
	}
	return aws.ToString(output.ETag), nil
}

func classifyObjectError(err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return fmt.Errorf("%w: %s", errObjectNotFound, apiErr.ErrorMessage())
		case "AccessDenied", "Forbidden", "403":
			return fmt.Errorf("%w: %s", errObjectAccessDenied, apiErr.ErrorMessage())
		case "PreconditionFailed", "ConditionalRequestConflict", "412", "409":
			return fmt.Errorf("%w: %s", errObjectPrecondition, apiErr.ErrorMessage())
		}
	}
	return err
}

type latestPointer struct {
	Version       int    `json:"version"`
	Generation    string `json:"generation,omitempty"`
	CommittedBy   string `json:"committedBy,omitempty"`
	CommittedAt   string `json:"committedAt,omitempty"`
	PriorETagHint string `json:"priorETagHint,omitempty"`
}

func readOrInitializePointer(ctx context.Context, objects objectStore, key string) (latestPointer, string, error) {
	data, etag, err := objects.get(ctx, key)
	if err == nil {
		pointer, decodeErr := decodePointer(data)
		return pointer, etag, decodeErr
	}
	if !errors.Is(err, errObjectNotFound) && !errors.Is(err, errObjectAccessDenied) {
		return latestPointer{}, "", fmt.Errorf("get latest pointer: %w", err)
	}
	empty := latestPointer{Version: 1}
	encoded, _ := json.Marshal(empty)
	encoded = append(encoded, '\n')
	etag, createErr := objects.putIfAbsent(ctx, key, encoded)
	if createErr == nil {
		return empty, etag, nil
	}
	if errors.Is(createErr, errObjectPrecondition) {
		data, etag, err = objects.get(ctx, key)
		if err != nil {
			return latestPointer{}, "", fmt.Errorf("get pointer after initialization race: %w", err)
		}
		pointer, decodeErr := decodePointer(data)
		return pointer, etag, decodeErr
	}
	if errors.Is(err, errObjectAccessDenied) {
		return latestPointer{}, "", fmt.Errorf("latest pointer get was ambiguous and conditional initialization failed; verify exact GetObject and PutObject permissions: %w", createErr)
	}
	return latestPointer{}, "", fmt.Errorf("initialize latest pointer: %w", createErr)
}

func decodePointer(data []byte) (latestPointer, error) {
	var pointer latestPointer
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pointer); err != nil {
		return latestPointer{}, fmt.Errorf("decode latest pointer: %w", err)
	}
	if pointer.Version != 1 {
		return latestPointer{}, fmt.Errorf("unsupported latest pointer version %d", pointer.Version)
	}
	if pointer.Generation != "" && (!strings.HasSuffix(pointer.Generation, ".json.gz") || strings.Contains(pointer.Generation, "/")) {
		return latestPointer{}, fmt.Errorf("invalid generation in latest pointer")
	}
	return pointer, nil
}
