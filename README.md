# VM File Restore Operator

A Kubernetes operator for KubeVirt that enables file-level restore operations for virtual machines from various backup sources.

## Description

The VM File Restore Operator provides a declarative way to restore individual files and directories to KubeVirt VirtualMachines from multiple source types:

- **PersistentVolumeClaims (PVCs)**: Restore from backup PVCs
- **VolumeSnapshots**: Restore from Kubernetes VolumeSnapshots
- **Remote Backups**: Restore from remote storage (S3, NFS, etc.)

This operator simplifies disaster recovery and file-level backup scenarios for virtualized workloads running on KubeVirt, enabling granular restore operations without needing to restore entire VM disk images.

## Features

- **Declarative File Restore**: Use Kubernetes CRs to restore files to running VMs
- **Multiple Source Types**: Restore from PVCs, VolumeSnapshots, or remote storage
- **Automatic and Manual Modes**: Automatic restore with specified paths, or manual mode for interactive restore
- **Hot-plug Technology**: No VM restart required - volumes are hot-plugged at runtime
- **Guest OS Auto-Detection**: Automatically detects Linux/Windows and adjusts mount paths
- **SSH-Based Execution**: Secure SSH access for executing restore commands in guest OS
- **Robust Error Handling**: Automatic retries, timeouts, and detailed error reporting
- **Idempotent Operations**: Safe to retry, handles partial failures gracefully

## Architecture

The operator uses a 9-phase state machine:

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
- go version v1.25.0+
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

The operator requires SSH access to VMs to execute restore operations. Follow these steps:

### 1. Get the Operator's SSH Public Key

After deploying the operator, retrieve the public key:

```bash
kubectl get configmap vm-file-restore-operator-ssh \
  -n vm-file-restore-operator-system \
  -o jsonpath='{.data.ssh-publickey}'
```

### 2. Add Public Key to Your VMs

Add the public key to `~/.ssh/authorized_keys` in each VM where you want to perform restores:

**For Linux VMs:**
```bash
# SSH into your VM
ssh user@vm-ip

# Add the operator's public key
echo "ssh-ed25519 AAAA...xyz vm-file-restore-operator" >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
```

**For Windows VMs:**
Add the key to `C:\ProgramData\ssh\administrators_authorized_keys` or the user's `.ssh\authorized_keys`.

### 3. Install Helper Scripts in VMs

The operator requires helper scripts installed in VMs:

**Linux:** `/usr/local/bin/filerestore.sh`  
**Windows:** `C:\Program Files\filerestore\filerestore.bat`

See `docs/` for helper script installation instructions.

### 4. Create a Restore

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

- **virtualMachineName**: Target VM to restore files into
- **source**: One of PVC, VolumeSnapshot, or RemoteBackup
- **files**: List of specific file paths to restore
- **directories**: List of directories to restore recursively
- **targetVolume**: (Optional) Specific volume in the VM to restore to

### Examples

#### Restore from PVC

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: restore-from-pvc
spec:
  virtualMachineName: my-vm
  source:
    persistentVolumeClaim:
      name: backup-pvc
      namespace: default
  files:
    - /etc/important-config.conf
  directories:
    - /var/lib/data
```

#### Restore from VolumeSnapshot

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: restore-from-snapshot
spec:
  virtualMachineName: my-vm
  source:
    volumeSnapshot:
      name: vm-snapshot-20260415
  files:
    - /etc/database/db.conf
  targetVolume: data-volume
```

#### Restore from Remote Backup

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: restore-from-remote
spec:
  virtualMachineName: my-vm
  source:
    remoteBackup:
      url: s3://my-bucket/backups/vm-backup.tar.gz
      secretRef:
        name: s3-credentials
  directories:
    - /opt/application/data
```

**Note:** Remote sources are planned but not yet implemented.

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
  # No sourcePath - manual mode
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

# vm-file-restore-operator
