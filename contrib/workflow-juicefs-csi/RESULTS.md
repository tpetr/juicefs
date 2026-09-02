# Dev2 proof results

Tests ran on 2026-09-02 in `dev2-semgrep-app-cluster`, namespace
`ai-workflows`, using the digest-pinned multi-architecture image
`sha256:f4efcfec770ca4f2756111212f891d3ff24a368fc51800cb5cbb6c1c6f847d1a`.
The application pods used `runtimeClassName: gvisor-netraw` and the existing
`ai-workflows` service account because dev2 has no `runtime-demo` service
account.

## Outcomes

- Architecture: passed. The gVisor pod created, read, appended, renamed,
  listed, and deleted a file. Before CSI publish returned, the node driver
  logged `JuiceFS mount is readable before publish returns`. The pod persisted
  success intent, teardown committed generation
  `20260902T182431.670649619Z-a1d251fa6afa08b0.json.gz`, and its Lease and local
  state were removed.
- Linear A to B: passed. A wrote `written-by-a`, marked success, and committed
  an immutable generation. B restored the dump with `juicefs load` and
  verified the file. Both pods landed on the same currently available gVisor
  node; cross-node placement remains unproven by this run.
- Failed attempt: passed. An unmarked writer changed the file to
  `uncommitted-failed-change`; teardown logged `discarding metadata because no
  success intent was recorded`. The exact pointer remained on
  `20260902T182508.922915619Z-f42c7ae9b5c5e74a.json.gz`, and the replacement
  observed `written-by-a`.
- Contention: passed. While the owner held Lease
  `workspace-09d6fbc166ab76fc8d7c6c5f` in `mounted` phase, kubelet repeatedly
  received CSI `Aborted` with `workspace lease held` for the challenger. The
  challenger never mounted concurrently.
- Cleanup: passed. All named test pods and workspace Leases were deleted. The
  driver state root was empty after teardown. The DaemonSet, CSIDriver, Role,
  and RoleBinding remain deployed for inspection; immutable POC objects remain
  deliberately retained.

All S3 verification used exact keys. The committed pointer and referenced dump
were fetched/headed without listing the bucket. The tested generation object
was 754 bytes with ETag `4e49394309b824bd257b8ee677921781`.

## Issues found during the proof

The first metadata run found that the canonical kubelet EmptyDir path exceeded
Linux's Unix-socket pathname limit. The final manifest mounts the same kubelet
pods hostPath at short `/k/pods` for the control endpoint while retaining the
canonical target for CSI validation and bind mounting.

It also showed that JuiceFS's generic format-time storage probe lists objects
and attempts to delete a temporary key. The final DaemonSet sets
`JFS_NO_CHECK_OBJECT_STORAGE=1`; protocol-specific exact conditional pointer
operations validate the required access instead. Two unreferenced temporary
`testing/<random>` objects from the failed pre-fix architecture attempts may
remain under the architecture-proof data prefix because the workload role has
no `s3:DeleteObject` permission.

## Verification

The nested module passed `gofmt`, `go test ./...`, `go vet ./...`, and
golangci-lint v2.6.0 (`0 issues`). All manifests passed Kubernetes server-side
dry-run, all deployed images are digest pinned, and `git diff --check` passed.
Production gaps and the node-loss ambiguity are listed in [DESIGN.md](DESIGN.md).
