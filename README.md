# VM File Restore Operator

A Kubernetes operator for KubeVirt that enables file-level restore operations for virtual machines from various backup sources.

## Description

The VM File Restore Operator provides a declarative way to restore individual files and directories to KubeVirt VirtualMachines from multiple source types:

- **PersistentVolumeClaims (PVCs)**: Restore from backup PVCs
- **VolumeSnapshots**: Restore from Kubernetes VolumeSnapshots
- **Remote Storage**: Restore from remote storage via rclone (S3, GCS, etc.)

This operator simplifies disaster recovery and file-level backup scenarios for virtualized workloads running on KubeVirt, enabling granular restore operations without needing to restore entire VM disk images.

## Features

- **Declarative File Restore**: Use Kubernetes CRs to restore files to running VMs
- **Multiple Source Types**: Restore from PVCs, VolumeSnapshots, or remote storage via rclone
- **Automatic and Manual Modes**: Automatic restore with `sourcePath`, or manual mode (omit `sourcePath`) for interactive restore
- **Hot-plug Technology**: No VM restart required - volumes are hot-plugged at runtime
- **Guest OS Auto-Detection**: Automatically detects Linux/Windows and adjusts mount paths
- **SSH-Based Execution**: Secure SSH access for executing restore commands in guest OS
- **Robust Error Handling**: Automatic retries, timeouts, and detailed error reporting
- **Idempotent Operations**: Safe to retry, handles partial failures gracefully
- **HCO Integration**: Can be managed by HyperConverged Cluster Operator with TLS security profiles

## Architecture

The operator uses a 10-phase state machine:

```
New → Init → Hotplugging → WaitingForAttachment → SSHConnecting → 
  Restoring → Cleanup → Succeeded
                    ↓
                  Failed
```

**How it works:**
1. **Init**: Validates target VM is running and source exists
2. **Hotplugging**: Modifies VM spec to add restore volume (hot-plug)
3. **WaitingForAttachment**: Waits for KubeVirt to attach volume to VMI
4. **SSHConnecting**: Establishes SSH connection to VM guest OS
5. **Restoring**: Executes helper script to mount and restore files
6. **Cleanup**: Unplugs volume from VM, deletes temporary PVCs
7. **Succeeded/Failed**: Terminal state with completion time

**Special Mode:**
- **VolumeReady**: Manual restore mode - volume stays attached until CR deletion

## Getting Started

### Prerequisites
- go version v1.26.0+
- docker version 17.03+
- kubectl version v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster with KubeVirt installed

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/file-restore-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/file-restore-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

## SSH Setup for File Restore

The operator requires SSH access to VMs to execute restore operations. The operator connects as user **`filerestore`** on both Linux and Windows VMs.

The operator stores everything needed for VM setup in its ConfigMap (`vm-file-restore-operator-ssh`):
- `data.ssh-publickey` — the operator's SSH public key
- `binaryData.linux-helpers.tar` — uncompressed tar with `setup.sh` and `filerestore.sh`
- `binaryData.windows-helpers.tar` — uncompressed tar with `setup.bat` and `filerestore.bat`

### 1. Configure VMs with Setup Scripts

Extract the scripts from the ConfigMap and run them on the VM — no internet access required in the guest.

**For Linux VMs:**
```bash
PUB_KEY=$(kubectl get configmap vm-file-restore-operator-ssh -n file-restore \
  -o jsonpath='{.data.ssh-publickey}')

mkdir -p /tmp/linux-helpers
kubectl get configmap vm-file-restore-operator-ssh -n file-restore \
  -o jsonpath="{.binaryData['linux-helpers\.tar']}" \
  | base64 -d | tar -xf - -C /tmp/linux-helpers/

virtctl scp --recursive /tmp/linux-helpers/ root@vmi/fedora:/tmp/
virtctl ssh root@vmi/fedora -c "bash /tmp/linux-helpers/setup.sh '$PUB_KEY'"
```

**For Windows VMs:**
```bash
PUB_KEY=$(kubectl get configmap vm-file-restore-operator-ssh -n file-restore \
  -o jsonpath='{.data.ssh-publickey}')

mkdir -p /tmp/win-helpers
kubectl get configmap vm-file-restore-operator-ssh -n file-restore \
  -o jsonpath="{.binaryData['windows-helpers\.tar']}" \
  | base64 -d | tar -xf - -C /tmp/win-helpers/

virtctl scp --recursive /tmp/win-helpers/ Administrator@vmi/win11:win-helpers
virtctl ssh Administrator@vmi/win11 -c "win-helpers\setup.bat \"$PUB_KEY\""
```

**What the setup scripts do:**
- Create the `filerestore` user with appropriate permissions
- Configure SSH key authentication (command-restricted for security)
- Set up passwordless sudo (Linux only, restricted to restore script)
- Install the helper script (`filerestore.sh` or `filerestore.bat`) from the same directory
- Verify the installation

**Manual setup:** If you prefer manual configuration or need to troubleshoot, see `guest-helpers/linux/setup.sh` and `guest-helpers/windows/setup.bat` for the detailed steps.

### 2. Create a Restore

Once SSH is configured and helpers are installed, create a restore:

```bash
kubectl apply -f config/samples/restore_v1alpha1_virtualmachinefilerestore.yaml
```

Monitor progress:

```bash
kubectl get vmfr -w
kubectl describe vmfr <restore-name>
```

## Usage

### VirtualMachineFileRestore API

The `VirtualMachineFileRestore` CRD allows you to specify:

- **target**: Reference to the target VirtualMachine (apiGroup, kind, name)
- **source**: One of `pvc`, `snapshot`, or `remote`
- **sourcePath**: File or directory path to restore from the backup (omit for manual mode)

### Examples

#### Restore from PVC

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: restore-from-pvc
spec:
  target:
    apiGroup: kubevirt.io
    kind: VirtualMachine
    name: my-vm
  source:
    pvc:
      name: backup-pvc
  sourcePath: /home/user/data
```

#### Restore from VolumeSnapshot

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: restore-from-snapshot
spec:
  target:
    apiGroup: kubevirt.io
    kind: VirtualMachine
    name: my-vm
  source:
    snapshot:
      name: vm-snapshot-20260415
  sourcePath: /etc/database/db.conf
```

#### Restore from Remote Storage (via rclone)

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: restore-from-remote
spec:
  target:
    apiGroup: kubevirt.io
    kind: VirtualMachine
    name: my-vm
  source:
    remote:
      name: s3_backup
      bucket: my-bucket
  sourcePath: /home/user/data
```

**Note:** Remote sources require rclone to be configured in the guest VM with the named remote.

#### Manual Restore Mode

Omit `sourcePath` to hotplug the volume without automatic restore. The volume stays attached in `VolumeReady` phase until you delete the CR:

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: manual-restore
spec:
  target:
    apiGroup: kubevirt.io
    kind: VirtualMachine
    name: fedora
  source:
    snapshot:
      name: snap1
```

In manual mode:
1. Volume is hotplugged and mounted at `/backup` (Linux) or `C:\backup` (Windows)
2. CR stays in `VolumeReady` phase
3. SSH into VM and manually copy files
4. Delete CR to unplug volume and clean up

### Check Restore Status

```sh
kubectl get vmfr
kubectl describe vmfr restore-from-pvc
```

The status shows the current phase and progress:

**Phases:**
- `New` - CR created, not yet started
- `Init` - Validating target VM and source
- `Hotplugging` - Attaching restore volume to VM
- `WaitingForAttachment` - Waiting for volume to attach (max 5 minutes)
- `SSHConnecting` - Establishing SSH connection (max 2 minutes with retry)
- `Restoring` - Executing file restore command
- `VolumeReady` - Manual restore mode (sourcePath empty), volume is mounted
- `Cleanup` - Unplugging volume and cleaning up
- `Succeeded` - Restore completed successfully
- `Failed` - Restore failed (see errorMessage for details)

**Status Fields:**
- `phase` - Current phase of the restore
- `startTime` - When the restore started
- `completionTime` - When the restore completed
- `restoredFilesCount` - Number of files restored
- `mountPath` - Where the volume is mounted in guest OS
- `errorMessage` - Details if restore failed
- `conditions` - Additional status information

**Timeouts and Retries:**
- Volume attachment: 5-minute timeout with exponential backoff
- SSH connection: 2-minute timeout with retry
- All operations are idempotent and safe to retry

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/file-restore-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/file-restore-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
operator-sdk edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Testing

### Unit Tests

Runs Go unit tests and containerized guest-helper script tests (requires docker or podman):

```bash
make test
```

### E2E Tests

E2e tests exercise the operator against a real KubeVirt cluster created by
[kubevirtci](https://github.com/kubevirt/kubevirtci). They require a running
cluster, a deployed operator, and `virtctl` on `PATH`.

#### Make targets

| Target | What it does |
|--------|----------------|
| `make cluster-functest` | Full workflow: tear down any existing cluster, bring up kubevirtci, build and deploy the operator, run e2e tests (`hack/test-e2e.sh`). |
| `make test-e2e` | Run the e2e test suite only. Cluster must already be up and the operator deployed. |
| `make cluster-up` | Create the kubevirtci cluster (KubeVirt, CDI, storage). |
| `make cluster-sync` | Build the operator image, push it to the kubevirtci registry, apply manifests, restart the deployment. |
| `make cluster-down` | Tear down the kubevirtci cluster. |
| `make cluster-image-env` | Print `export PUSH_IMG=...` and `export IMG=...` for the current kubevirtci registry. |

Run `make help` for the full target list.

#### Operator image during e2e

E2e does **not** deploy a pre-built image from Quay. `cluster-sync` builds the
operator from the current working tree, tags it with the current git commit
(`dev-<short-sha>` by default), and uses kubevirtci's embedded registry:

| Variable | Used for | Typical value |
|----------|----------|---------------|
| `PUSH_IMG` | `docker build` / `docker push` from the host | `localhost:<port>/kubevirt/vm-file-restore-operator:dev-<sha>` |
| `IMG` | Image reference written into `dist/install.yaml` | `registry:5000/kubevirt/vm-file-restore-operator:dev-<sha>` |

The host and in-cluster URLs differ because kubevirtci exposes the registry on
`localhost:<port>` outside the cluster and as `registry:5000` inside it. Values
are read from `kubevirtci/_ci-configs/<provider>/config-provider-*.sh` after
`cluster-up`; see `hack/kubevirtci-image-env.sh`.

Set both explicitly to use an external registry instead:

```bash
PUSH_IMG=registry.example.com/kubevirt/vm-file-restore-operator:example-tag \
IMG=registry:5000/kubevirt/vm-file-restore-operator:example-tag \
make cluster-sync
```

#### Run the full suite locally

```bash
make cluster-functest
```

Equivalent to:

```bash
./hack/test-e2e.sh
```

Requires `docker`, `kubectl`, `virtctl`, and sufficient resources for a
2-node cluster with rook-ceph storage (defaults match the prow job:
`KUBEVIRT_NUM_NODES=2`, `KUBEVIRT_STORAGE=rook-ceph-default`).

#### Run step-by-step (iteration)

Use this when the cluster is already up and you want to redeploy or re-run tests
without recreating the cluster.

```bash
make cluster-up

kubeconfig="$(source hack/config.sh && ./kubevirtci/cluster-up/kubeconfig.sh)"
export KUBECONFIG="${kubeconfig}"
kubectl get nodes

make cluster-sync
make test-e2e
```

Rebuild the manifest and restart the deployment without rebuilding the image
(useful when only `test/e2e/` changed):

```bash
SKIP_IMAGE_BUILD=true make cluster-sync
make test-e2e
```

Run a subset of tests:

```bash
E2E_TEST_CMD='go test ./test/e2e/ -v -ginkgo.v -timeout=90m -ginkgo.focus="manual restore"' make test-e2e
```

Default test timeout is `90m` (`E2E_TIMEOUT`).

#### CI

Prow job `pull-vm-file-restore-operator-e2e` runs `./hack/test-e2e.sh` on bare-metal
workers. The job is optional and not run on every PR; trigger it with:

```text
/test pull-vm-file-restore-operator-e2e
```

#### Cluster options

```bash
KUBEVIRT_VERSION=v1.9.0 make cluster-up          # KubeVirt release (default: v1.8.4)
KUBEVIRTCI_TAG=<tag> make cluster-up             # kubevirtci gocli version
KUBEVIRT_PROVIDER=k8s-1.37 make cluster-up       # Kubernetes version (default: k8s-1.36)
KUBEVIRT_NUM_NODES=3 make cluster-up             # cluster size (default: 2)
KUBEVIRT_WAIT_TIMEOUT=15m make cluster-up        # KubeVirt/CDI readiness (default: 10m)
```

#### Tear down

```bash
make cluster-down
```

## Development

### Linting

Run golangci-lint:
```bash
make lint
```

Fix linting issues automatically:
```bash
make lint-fix
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
