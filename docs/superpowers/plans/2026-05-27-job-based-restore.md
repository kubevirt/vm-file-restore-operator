# Job-Based SSH Execution Implementation Plan

## Context

**Problem:** The VM File Restore Operator currently executes long-running SSH commands synchronously within the reconciliation loop. When a restore operation takes 10-60 minutes, the reconciler blocks on `sshClient.RunCommand(ctx, command)` (phases.go:571), which:

1. **Blocks the worker pool**: Controller-runtime typically uses 1 worker per controller, preventing other VirtualMachineFileRestore CRs from being processed
2. **No timeout enforcement**: Context is passed through with no timeout, so hung SSH commands can block indefinitely
3. **Poor resource utilization**: Operator pod holds connections and memory while waiting for remote operations
4. **Difficult troubleshooting**: Logs are only available after completion, no progress visibility

**Proposed Solution:** Replace synchronous SSH execution with asynchronous Kubernetes Job execution. The reconciler creates a Job to run the restore command, then polls Job status every 10 seconds instead of blocking. This decouples long-running operations from the reconciliation loop.

**Scope:** This plan covers Job-based execution for the **Restoring phase only** (automatic file restore). Manual restore mode (VolumeReady phase) and cleanup operations remain unchanged in this iteration.

---

## Complexity Analysis

### Overall Complexity: **MODERATE-HIGH** (6-8 / 10)

| Aspect | Complexity | Reason |
|--------|-----------|--------|
| **Conceptual** | Medium | Job pattern is well-known in K8s, but integrating with state machine requires careful design |
| **Implementation** | Medium-High | ~500-800 lines of new code across 8 files, new image build pipeline |
| **Testing** | High | Requires mocking Job lifecycle, Pod logs API, timing-dependent scenarios |
| **Deployment** | Medium | New image to build/push, additional RBAC permissions, backward compatibility concerns |
| **Risk** | Medium | Owner references and TTL provide safety net, but async operations add failure modes |

**Time Estimate:** 
- Implementation: 2-3 days (with tests)
- E2E testing: 1 day
- Documentation: 0.5 day
- **Total: 3.5-4.5 days**

---

## Architecture Overview

### Current Flow
```
SSHConnecting → Restoring (blocks 10-60 min) → Cleanup → Succeeded
```

### New Flow
```
SSHConnecting → JobCreating → JobRunning (poll every 10s) → JobCompleting → Cleanup → Succeeded
                     ↓              ↓                            ↓
                  Create Job    Monitor Job              Parse logs & extract file count
```

### Job Lifecycle
1. **JobCreating phase**: Reconciler creates Job with owner reference
2. **JobRunning phase**: Poll `job.Status` every 10s, wait for Succeeded/Failed
3. **JobCompleting phase**: Fetch Pod logs, parse file count, transition to Cleanup
4. **Automatic cleanup**: TTL controller deletes Job 5 min after completion

---

## Implementation Components

### 1. New Container Image: `restore-job`

**Location:** `restore-job/Dockerfile` (new directory)

**Base:** Alpine Linux 3.20 (~5MB base + 3MB openssh-client = 8MB total)

**Contents:**
- openssh-client package
- Shell script: `/restore-runner.sh`

**Script logic:**
```bash
#!/bin/sh
set -e

# Environment variables injected by Job spec:
# VM_IP, OS_TYPE, VOLUME_NAME, MOUNT_PATH, SOURCE_PATH

# Build restore command based on OS
if [ "$OS_TYPE" = "windows" ]; then
    RESTORE_CMD="\"C:\\Program Files\\filerestore\\filerestore.bat\" restore --serial ${VOLUME_NAME} --mount-path \"${MOUNT_PATH}\" --source-path \"${SOURCE_PATH}\""
else
    RESTORE_CMD="/usr/local/bin/filerestore.sh restore --serial ${VOLUME_NAME} --mount-path ${MOUNT_PATH} --source-path ${SOURCE_PATH}"
fi

# Execute via SSH
ssh -i /ssh/id_ed25519 \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=10 \
    filerestore@${VM_IP} \
    "${RESTORE_CMD}"
```

**Build/Push:**
- Makefile targets: `docker-build-job`, `docker-push-job`
- Image ref stored in env var `RESTORE_JOB_IMAGE` in manager deployment
- Default: `$(IMAGE_REGISTRY)/vm-file-restore-job:$(VERSION)`

**Why separate image?**
- Operator uses distroless/static (no shell, no ssh binary)
- Adding Alpine base to operator increases size 300% and adds attack surface
- Job image is purpose-built, minimal, independently versioned

---

### 2. CRD Status Field Additions

**File:** `api/v1alpha1/virtualmachinefilerestore_types.go`

Add to `VirtualMachineFileRestoreStatus` struct:

```go
// RestoreJobName is the name of the Kubernetes Job created for restore execution.
// Used to lookup Job status in subsequent reconciliations.
// +optional
RestoreJobName string `json:"restoreJobName,omitempty"`

// JobStartTime records when the restore Job was created.
// Used for timeout calculations (current time - JobStartTime > 60 min = timeout).
// +optional
JobStartTime *metav1.Time `json:"jobStartTime,omitempty"`
```

**Regenerate:** Run `make manifests generate` to update CRD YAML and deepcopy methods.

---

### 3. New Phase Constants

**File:** `api/v1alpha1/virtualmachinefilerestore_types.go`

Add to `RestorePhase` constants:

```go
// RestorePhaseJobCreating indicates the restore Job is being created
RestorePhaseJobCreating RestorePhase = "JobCreating"

// RestorePhaseJobRunning indicates the restore Job is executing
RestorePhaseJobRunning RestorePhase = "JobRunning"

// RestorePhaseJobCompleting indicates the Job succeeded and results are being processed
RestorePhaseJobCompleting RestorePhase = "JobCompleting"

// RestorePhaseRestoring is deprecated (kept for backward compat with existing CRs)
// New restores should use JobCreating → JobRunning → JobCompleting flow
RestorePhaseRestoring RestorePhase = "Restoring"
```

---

### 4. Job Creation Logic

**File:** `internal/controller/job.go` (new file, ~200 lines)

**Functions:**

```go
// BuildRestoreJob constructs a Job spec for executing restore via SSH
func BuildRestoreJob(
    vmfr *restorev1alpha1.VirtualMachineFileRestore,
    vmIP string,
    osType string,
    volumeName string,
    jobImage string,
) *batchv1.Job

// CreateRestoreJob creates the Job and sets owner reference
func CreateRestoreJob(
    ctx context.Context,
    c client.Client,
    vmfr *restorev1alpha1.VirtualMachineFileRestore,
    job *batchv1.Job,
) error

// GetRestoreJobStatus fetches Job and returns status summary
func GetRestoreJobStatus(
    ctx context.Context,
    c client.Client,
    jobName string,
    namespace string,
) (active int32, succeeded int32, failed int32, err error)

// FetchJobPodLogs retrieves logs from Job's Pod
func FetchJobPodLogs(
    ctx context.Context,
    c client.Client,
    jobName string,
    namespace string,
) (string, error)
```

**Job Spec Details:**
- Name: `<vmfr.Name>-restore-job`
- Owner reference: `vmfr` (automatic cleanup on CR deletion)
- `backoffLimit: 0` (no auto retries, operator handles retry logic)
- `activeDeadlineSeconds: 3600` (60 min hard timeout)
- `ttlSecondsAfterFinished: 300` (auto-delete 5 min after completion)
- Env vars: `VM_IP`, `OS_TYPE`, `VOLUME_NAME`, `MOUNT_PATH`, `SOURCE_PATH`
- Volume mount: Secret `vm-file-restore-operator-ssh` → `/ssh/id_ed25519`
- Security: `runAsUser: 65532`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`

---

### 5. Phase Handler Changes

**File:** `internal/controller/phases.go`

#### Modify: `handleSSHConnectingPhase`

**Current behavior:** After SSH connection succeeds, transitions to `RestorePhaseRestoring`

**New behavior:** Transition to `RestorePhaseJobCreating` instead

```go
// Line ~518: Change transition target
logger.Info("Automatic restore mode, transitioning to JobCreating")
return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseJobCreating, "SSH validated, creating restore Job")
```

#### New: `handleJobCreatingPhase`

```go
func handleJobCreatingPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
    logger := log.FromContext(ctx)
    
    // Check if Job already exists (idempotency)
    if vmfr.Status.RestoreJobName != "" {
        // Job was already created, transition to JobRunning
        return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseJobRunning, "Job already created")
    }
    
    // Get VM IP address from VMI
    vmi := &v1.VirtualMachineInstance{}
    if err := r.Get(ctx, client.ObjectKey{Name: vmfr.Spec.Target.Name, Namespace: vmfr.Namespace}, vmi); err != nil {
        return failRestore(ctx, r, vmfr, err, "failed to get VMI")
    }
    ip, err := GetVMIPAddress(ctx, r.Client, vmi)
    if err != nil {
        return failRestore(ctx, r, vmfr, err, "failed to get VM IP")
    }
    
    // Build and create Job
    osType, _ := DetectGuestOS(vmi)
    volumeName := GetVolumeName(vmfr.Name)
    job := BuildRestoreJob(vmfr, ip, osType, volumeName, r.restoreJobImage)
    
    if err := CreateRestoreJob(ctx, r.Client, vmfr, job); err != nil {
        if errors.IsAlreadyExists(err) {
            // Idempotent: Job exists from previous reconciliation
        } else {
            return failRestore(ctx, r, vmfr, err, "failed to create restore Job")
        }
    }
    
    // Update status with Job name and start time
    patch := client.MergeFrom(vmfr.DeepCopy())
    vmfr.Status.RestoreJobName = job.Name
    now := metav1.Now()
    vmfr.Status.JobStartTime = &now
    if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
        logger.Error(err, "Failed to update status with Job name")
        return ctrl.Result{}, err
    }
    
    logger.Info("Restore Job created", "jobName", job.Name)
    r.Recorder.Event(vmfr, corev1.EventTypeNormal, "JobCreated", fmt.Sprintf("Created restore Job: %s", job.Name))
    
    return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseJobRunning, "Restore Job created and running")
}
```

#### New: `handleJobRunningPhase`

```go
func handleJobRunningPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
    logger := log.FromContext(ctx)
    
    // Get Job status
    active, succeeded, failed, err := GetRestoreJobStatus(ctx, r.Client, vmfr.Status.RestoreJobName, vmfr.Namespace)
    if err != nil {
        return failRestore(ctx, r, vmfr, err, "failed to get Job status")
    }
    
    // Check for timeout (60 minutes)
    if vmfr.Status.JobStartTime != nil {
        elapsed := time.Since(vmfr.Status.JobStartTime.Time)
        if elapsed > 60*time.Minute {
            // Delete timed-out Job
            job := &batchv1.Job{}
            jobKey := client.ObjectKey{Name: vmfr.Status.RestoreJobName, Namespace: vmfr.Namespace}
            if err := r.Get(ctx, jobKey, job); err == nil {
                propagation := metav1.DeletePropagationForeground
                r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation})
            }
            return failRestore(ctx, r, vmfr, fmt.Errorf("restore timeout"), 
                fmt.Sprintf("Restore Job exceeded 60 minute timeout (elapsed: %v)", elapsed))
        }
    }
    
    // Check Job completion status
    if succeeded > 0 {
        logger.Info("Restore Job succeeded", "jobName", vmfr.Status.RestoreJobName)
        return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseJobCompleting, "Restore Job completed successfully")
    }
    
    if failed > 0 {
        // Fetch Pod logs for error details
        logs, logErr := FetchJobPodLogs(ctx, r.Client, vmfr.Status.RestoreJobName, vmfr.Namespace)
        if logErr != nil {
            logger.Error(logErr, "Failed to fetch Job Pod logs")
            logs = "(logs unavailable)"
        }
        truncatedLogs := TruncateOutput(logs, 50)
        return failRestore(ctx, r, vmfr, fmt.Errorf("restore Job failed"), 
            fmt.Sprintf("Restore Job failed\n%s", truncatedLogs))
    }
    
    // Job still active/running
    logger.Info("Restore Job still running", "jobName", vmfr.Status.RestoreJobName, "active", active)
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}
```

#### New: `handleJobCompletingPhase`

```go
func handleJobCompletingPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
    logger := log.FromContext(ctx)
    
    // Fetch Pod logs
    logs, err := FetchJobPodLogs(ctx, r.Client, vmfr.Status.RestoreJobName, vmfr.Namespace)
    if err != nil {
        logger.Error(err, "Failed to fetch Job logs, defaulting file count to 0")
        logs = ""
    }
    
    // Parse file count from logs (reuse existing logic from handleRestoringPhase)
    fileCount := int32(0)
    for _, line := range strings.Split(logs, "\n") {
        var count int32
        if n, _ := fmt.Sscanf(line, "%d files restored", &count); n == 1 {
            fileCount = count
            break
        }
        if n, _ := fmt.Sscanf(line, "Restored %d files", &count); n == 1 {
            fileCount = count
            break
        }
        if n, _ := fmt.Sscanf(line, "%d files", &count); n == 1 {
            fileCount = count
            break
        }
    }
    
    logger.Info("Parsed restore results from Job logs", "filesRestored", fileCount)
    
    // Update file count and transition to Cleanup
    patch := client.MergeFrom(vmfr.DeepCopy())
    vmfr.Status.RestoredFilesCount = fileCount
    vmfr.Status.Phase = restorev1alpha1.RestorePhaseCleanup
    
    if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
        logger.Error(err, "Failed to update status during phase transition to Cleanup")
        return ctrl.Result{}, err
    }
    
    r.Recorder.Event(vmfr, corev1.EventTypeNormal, string(restorev1alpha1.RestorePhaseCleanup),
        fmt.Sprintf("Restored %d files, cleaning up", fileCount))
    
    return ctrl.Result{Requeue: true}, nil
}
```

#### Deprecate: `handleRestoringPhase`

Keep function for backward compatibility with existing CRs in Restoring phase:

```go
func handleRestoringPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
    logger := log.FromContext(ctx)
    
    // Backward compat: CRs created before Job implementation
    logger.Info("Legacy Restoring phase detected, transitioning to JobCreating")
    return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseJobCreating, "Migrating to Job-based restore")
}
```

#### Update: `getPhaseHandler`

Add new phase mappings:

```go
case restorev1alpha1.RestorePhaseJobCreating:
    return handleJobCreatingPhase
case restorev1alpha1.RestorePhaseJobRunning:
    return handleJobRunningPhase
case restorev1alpha1.RestorePhaseJobCompleting:
    return handleJobCompletingPhase
```

---

### 6. RBAC Permissions

**File:** `internal/controller/virtualmachinefilerestore_controller.go`

Add kubebuilder RBAC markers:

```go
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
```

Run `make manifests` to regenerate `config/rbac/role.yaml`.

---

### 7. Reconciler Configuration

**File:** `internal/controller/virtualmachinefilerestore_controller.go`

Add field to reconciler struct:

```go
type VirtualMachineFileRestoreReconciler struct {
    client.Client
    Scheme   *runtime.Scheme
    Recorder record.EventRecorder
    
    // Image to use for restore Job Pods
    restoreJobImage string
}
```

**File:** `cmd/main.go`

Inject image from environment variable:

```go
restoreJobImage := os.Getenv("RESTORE_JOB_IMAGE")
if restoreJobImage == "" {
    restoreJobImage = "quay.io/kubevirt/vm-file-restore-job:latest"
    setupLog.Info("RESTORE_JOB_IMAGE not set, using default", "image", restoreJobImage)
}

if err = (&controller.VirtualMachineFileRestoreReconciler{
    Client:          mgr.GetClient(),
    Scheme:          mgr.GetScheme(),
    Recorder:        mgr.GetEventRecorderFor("virtualmachinefilerestore-controller"),
    restoreJobImage: restoreJobImage,
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "unable to create controller", "controller", "VirtualMachineFileRestore")
    os.Exit(1)
}
```

---

### 8. Cleanup Handler Updates

**File:** `internal/controller/virtualmachinefilerestore_controller.go`

Modify `cleanup()` function to delete Job if exists:

```go
func (r *VirtualMachineFileRestoreReconciler) cleanup(ctx context.Context, vmfr *restorev1alpha1.VirtualMachineFileRestore) error {
    logger := log.FromContext(ctx)
    
    // Delete restore Job if it exists
    if vmfr.Status.RestoreJobName != "" {
        job := &batchv1.Job{}
        jobKey := client.ObjectKey{
            Name:      vmfr.Status.RestoreJobName,
            Namespace: vmfr.Namespace,
        }
        err := r.Get(ctx, jobKey, job)
        if err == nil {
            logger.Info("Deleting restore Job during cleanup", "jobName", job.Name)
            propagation := metav1.DeletePropagationForeground
            if err := r.Delete(ctx, job, &client.DeleteOptions{
                PropagationPolicy: &propagation,
            }); err != nil && !errors.IsNotFound(err) {
                logger.Error(err, "Failed to delete Job", "jobName", job.Name)
                // Continue with cleanup even if Job deletion fails
            }
        } else if !errors.IsNotFound(err) {
            logger.Error(err, "Failed to get Job for deletion", "jobName", vmfr.Status.RestoreJobName)
        }
    }
    
    // ... existing cleanup logic (unplug volume, SSH cleanup) ...
}
```

---

### 9. Build and Deployment Changes

**File:** `Makefile`

Add targets for restore-job image:

```makefile
RESTORE_JOB_IMG ?= $(IMAGE_TAG_BASE)-job:$(VERSION)

.PHONY: docker-build-job
docker-build-job: ## Build restore-job container image
	$(CONTAINER_TOOL) build -t $(RESTORE_JOB_IMG) ./restore-job

.PHONY: docker-push-job
docker-push-job: ## Push restore-job container image
	$(CONTAINER_TOOL) push $(RESTORE_JOB_IMG)

.PHONY: docker-build-all
docker-build-all: docker-build docker-build-job ## Build both operator and restore-job images

.PHONY: docker-push-all
docker-push-all: docker-push docker-push-job ## Push both operator and restore-job images
```

**File:** `config/manager/manager.yaml`

Add environment variable to operator deployment:

```yaml
containers:
- name: manager
  env:
  - name: OPERATOR_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: RESTORE_JOB_IMAGE
    value: quay.io/kubevirt/vm-file-restore-job:latest  # Override via kustomize
```

---

## Testing Strategy

### Unit Tests

**File:** `internal/controller/job_test.go` (new)

```go
func TestBuildRestoreJob(t *testing.T) {
    // Verify Job spec, env vars, owner reference, volume mounts
}

func TestCreateRestoreJob_Idempotency(t *testing.T) {
    // Verify AlreadyExists errors are handled
}

func TestGetRestoreJobStatus(t *testing.T) {
    // Test active, succeeded, failed states
}

func TestFetchJobPodLogs(t *testing.T) {
    // Mock Pod logs API, test log retrieval
}
```

**File:** `internal/controller/phases_test.go` (extend)

```go
func TestHandleJobCreatingPhase(t *testing.T) {
    // Verify Job creation, status update, idempotency
}

func TestHandleJobRunningPhase_Timeout(t *testing.T) {
    // Verify timeout detection and Job deletion
}

func TestHandleJobRunningPhase_Success(t *testing.T) {
    // Verify transition to JobCompleting
}

func TestHandleJobCompletingPhase_ParseLogs(t *testing.T) {
    // Verify file count parsing from logs
}
```

### Integration Tests (envtest)

**File:** `internal/controller/suite_test.go` (extend)

- Mock Job creation and status updates
- Simulate Job lifecycle: Pending → Running → Succeeded
- Test timeout handling with fake time
- Verify owner references and cleanup

### E2E Tests

**File:** `test/e2e/e2e_test.go` (extend)

```go
It("should restore files via Job", func() {
    By("creating a VirtualMachineFileRestore CR")
    // ...
    
    By("verifying Job is created")
    Eventually(func() error {
        job := &batchv1.Job{}
        return k8sClient.Get(ctx, client.ObjectKey{
            Name: vmfr.Name + "-restore-job",
            Namespace: namespace,
        }, job)
    }).Should(Succeed())
    
    By("waiting for Job to complete")
    Eventually(func() int32 {
        job := &batchv1.Job{}
        k8sClient.Get(ctx, ...)
        return job.Status.Succeeded
    }, 10*time.Minute).Should(Equal(int32(1)))
    
    By("verifying VMFR transitions to Succeeded")
    Eventually(func() restorev1alpha1.RestorePhase {
        k8sClient.Get(ctx, ..., vmfr)
        return vmfr.Status.Phase
    }).Should(Equal(restorev1alpha1.RestorePhaseSucceeded))
})
```

---

## Verification Steps

### Manual Testing Checklist

1. **Build and push images:**
   ```bash
   make docker-build-all IMG=quay.io/<user>/vm-file-restore-operator:test
   make docker-push-all IMG=quay.io/<user>/vm-file-restore-operator:test
   ```

2. **Deploy operator:**
   ```bash
   make deploy IMG=quay.io/<user>/vm-file-restore-operator:test
   kubectl set env deployment/vm-file-restore-operator \
     -n file-restore \
     RESTORE_JOB_IMAGE=quay.io/<user>/vm-file-restore-job:test
   ```

3. **Create test VMFR:**
   ```bash
   kubectl apply -f examples/vmfr-job-test.yaml
   ```

4. **Watch lifecycle:**
   ```bash
   kubectl get job,pod,vmfr -n <namespace> -w
   ```

5. **Verify Job execution:**
   ```bash
   # Check Job was created
   kubectl get job <vmfr-name>-restore-job -n <namespace>
   
   # Check Job Pod logs
   kubectl logs job/<vmfr-name>-restore-job -n <namespace>
   
   # Verify VMFR status
   kubectl get vmfr <name> -n <namespace> -o yaml
   ```

6. **Test timeout:**
   - Create VMFR with non-existent VM (SSH will timeout)
   - Verify Job deleted after 60 min
   - Verify VMFR transitions to Failed

7. **Test cleanup:**
   ```bash
   kubectl delete vmfr <name> -n <namespace>
   # Verify Job is auto-deleted (owner reference)
   kubectl get job -n <namespace>
   ```

### Expected Behavior

✅ **Success path:**
1. VMFR created → Phase: Init
2. Volume hotplugged → Phase: WaitingForAttachment
3. SSH validated → Phase: JobCreating
4. Job created → Phase: JobRunning (poll every 10s)
5. Job succeeds → Phase: JobCompleting
6. Logs parsed → Phase: Cleanup
7. Volume unplugged → Phase: Succeeded

✅ **Timeout path:**
1. Job running > 60 min
2. Job deleted
3. VMFR → Phase: Failed, message: "Restore Job exceeded 60 minute timeout"

✅ **Cleanup path:**
1. VMFR deleted mid-restore
2. Finalizer triggers cleanup
3. Job deleted (foreground propagation)
4. Pod terminated
5. Volume unplugged

---

## Backward Compatibility

### Existing CRs in Restoring Phase

**Scenario:** Operator upgraded while CR is in `Restoring` phase (old synchronous SSH execution)

**Handling:**
- `handleRestoringPhase` detects phase, immediately transitions to `JobCreating`
- New Job is created to complete the restore
- No data loss, operation continues seamlessly

### Status Field Compatibility

**New fields:** `restoreJobName`, `jobStartTime` are optional (`+optional` tag)

**Impact:** No breaking changes, old CRs can be read/updated

---

## Rollback Plan

If Job-based implementation causes issues:

1. **Revert code:** Git revert the Job implementation commits
2. **Redeploy operator:** `make deploy IMG=<previous-version>`
3. **Existing CRs:** Will fall back to synchronous SSH execution (handleRestoringPhase still exists)
4. **No data loss:** Job-based restore is functionally equivalent, just async

---

## Future Enhancements

**Out of scope for this plan, consider later:**

1. **Job-based cleanup**: Apply same pattern to cleanup phase SSH command
2. **Progress reporting**: Update VMFR status with intermediate progress (requires helper script changes)
3. **Parallel restores**: Support multiple concurrent restore Jobs per operator instance
4. **Job Pod affinity**: Schedule Job Pods on same node as VM for faster network access
5. **Metrics**: Track Job success rate, average duration, timeout frequency

---

## Files Modified/Created

### Modified (8 files)
1. `api/v1alpha1/virtualmachinefilerestore_types.go` - Add status fields & phase constants
2. `internal/controller/phases.go` - Add 3 new phase handlers, modify SSHConnecting
3. `internal/controller/virtualmachinefilerestore_controller.go` - Add Job cleanup, inject image config
4. `cmd/main.go` - Read RESTORE_JOB_IMAGE env var
5. `Makefile` - Add docker-build-job, docker-push-job targets
6. `config/manager/manager.yaml` - Add RESTORE_JOB_IMAGE env var
7. `config/rbac/role.yaml` - Auto-generated (add batch permissions)
8. `test/e2e/e2e_test.go` - Add Job-based restore test

### Created (4 files)
1. `internal/controller/job.go` - Job creation, status checking, log fetching (~200 lines)
2. `internal/controller/job_test.go` - Unit tests for job.go (~300 lines)
3. `restore-job/Dockerfile` - Job image definition (~15 lines)
4. `restore-job/restore-runner.sh` - SSH execution script (~30 lines)

**Total:** ~800-1000 lines of new code + tests

---

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| Job scheduling failures (quota, node pressure) | Medium | Medium | Retry logic in JobCreating phase, clear error messages |
| TTL controller not cleaning up Jobs | Low | Low | Finalizer cleanup as backup |
| Pod logs unavailable (TTL deleted Pod) | Medium | Low | Best-effort log parsing, default fileCount=0 |
| Timeout too short/long for real-world restores | Medium | Medium | Make timeout configurable via annotation in future |
| Image pull failures for restore-job | Medium | High | Document image pre-pulling, use imagePullPolicy: IfNotPresent |
| Backward compat issues with existing CRs | Low | High | Keep handleRestoringPhase for migration path |

---

## Summary

**Complexity:** Moderate-High (6-8/10) - Requires new image, Job lifecycle management, async state machine changes, comprehensive testing

**Effort:** 3.5-4.5 days (including tests and documentation)

**Benefits:**
- Non-blocking reconciliation (scales to multiple concurrent restores)
- Enforced 60-minute timeout
- Better observability (Job status, Pod logs)
- Resource efficiency (operator pod not blocked)

**Tradeoffs:**
- Additional complexity (Job lifecycle, async operations)
- Separate image to build/maintain
- More failure modes to handle
- Testing complexity increases

**Recommendation:** Proceed with implementation if concurrent restore operations are expected or if 10+ minute restore times are common. For low-volume, short-duration restores (<5 min), Option 1 (timeout context) is simpler and sufficient.
