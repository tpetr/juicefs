# Workflow JuiceFS CSI prototype design

## Decision

The prototype is a small node-only CSI driver in an isolated Go module under
`contrib/`. It runs the repository's JuiceFS client as a child process of the
privileged node DaemonSet. `NodePublishVolume` formats or restores SQLite
metadata, mounts JuiceFS on a private host-propagated path, verifies the mount
is readable, bind-mounts it to the kubelet target, and only then returns.

This directly tests the ordering property that the previous FD-passing design
could not provide: gVisor creates its gofer view after the final filesystem is
already present at the CSI target.

The nested module keeps CSI, Kubernetes, and AWS SDK dependencies out of the
main JuiceFS binary. The initial architecture proof intentionally implements
only a fresh SQLite metadata database and clean `juicefs umount --flush`.
Metadata generations, success intent, and leases are gated on a successful
gVisor test.

## Process mode versus Mount Pods

Process mode has the smallest proof surface and avoids granting pod-creation
RBAC before the mount ordering is known to work. Its important weakness is
that a node-driver restart kills all child mount processes. The POC detects a
mounted target without an in-memory session and fails instead of pretending it
recovered.

The durable design should use one non-gVisor Mount Pod per workspace, pinned to
the requesting node and created with the requesting pod's service account only
after fetching and verifying that pod through the Kubernetes API. That gives
per-workspace resources and AWS identity, and it decouples mounts from driver
rollouts. It requires narrow pod create/get/delete RBAC and careful prevention
of arbitrary service-account selection.

## Reuse assessment

- The Semgrep `fuse-fd-csi-driver` provides useful CSI registration, strict
  kubelet target validation, idempotency, and bounded lifecycle patterns. Its
  defining FD handshake cannot satisfy the required ordering because the FUSE
  server starts in the workload.
- The official JuiceFS CSI driver already supports process and Mount Pod modes,
  but its controller/webhook/Secret/PV feature set is much broader than this
  single-writer workspace protocol. Extending it would couple Semgrep-specific
  success and immutable-generation semantics to a general-purpose driver.
- A small Semgrep-specific driver is therefore the cleanest prototype. A
  production implementation should still reuse or upstream narrowly useful
  official-driver mount helpers rather than copying its full control plane.

## Phase 1 mount sequence

1. Validate the kubelet target and kubelet-injected pod identity.
2. Create a per-volume directory below a fixed hostPath.
3. Format fresh SQLite metadata with S3 data storage. The fork's immutable
   `--bucket-prefix` scopes data below a pod-specific exact prefix.
4. Start `juicefs mount --foreground` in the node container.
5. Require a real mount-table entry, inode 1, and a successful directory read.
6. Bind mount the private mount to the kubelet target and read it again.
7. Return from `NodePublishVolume`.

On unpublish, the driver removes the bind mount, runs `juicefs umount --flush`,
waits for the foreground process, and removes only its hashed state directory.

## Security boundary in Phase 1

The bucket, region, root prefix, namespace, and service account are driver
configuration, not volume attributes. The only workspace identifier is the
kubelet-injected pod UID, cross-checked against the canonical CSI target.
Arbitrary host paths, path traversal, and cross-namespace use are rejected.
No command receives credentials as arguments and command output is bounded.

The dev POC uses the existing `ai-workflows` service account because no
`runtime-demo` service account exists in dev2. Although that role currently
has `s3:ListBucket`, neither the driver nor its manifests invoke a list
operation. Production policy should remove that permission for the workspace
prefix.
