# VM File Restore Operator

A Kubernetes operator for KubeVirt that enables file-level restore operations for virtual machines from various backup sources.

## Description

The VM File Restore Operator provides a declarative way to restore individual files and directories to KubeVirt VirtualMachines from multiple source types:

- **PersistentVolumeClaims (PVCs)**: Restore from backup PVCs
- **VolumeSnapshots**: Restore from Kubernetes VolumeSnapshots
- **Remote Backups**: Restore from remote storage (S3, NFS, etc.)

This operator simplifies disaster recovery and file-level backup scenarios for virtualized workloads running on KubeVirt, enabling granular restore operations without needing to restore entire VM disk images.

## Getting Started

### Prerequisites
- go version v1.24.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

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
apiVersion: restore.kubevirt.io/v1alpha1
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
apiVersion: restore.kubevirt.io/v1alpha1
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
apiVersion: restore.kubevirt.io/v1alpha1
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

### Check Restore Status

```sh
kubectl get vmfr
kubectl describe vmfr restore-from-pvc
```

The status will show the current phase (New, InProgress, Succeeded, Failed) and the number of files restored.

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
