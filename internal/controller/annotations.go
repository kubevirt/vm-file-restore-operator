/*
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
*/

package controller

import (
	"strconv"
	"time"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

const (
	annotationAttachmentRetries   = "vmfr.kubevirt.io/attachment-retries"
	annotationSSHRetries          = "vmfr.kubevirt.io/ssh-retries"
	annotationLastAttachmentCheck = "vmfr.kubevirt.io/last-attachment-check"
	annotationLastSSHCheck        = "vmfr.kubevirt.io/last-ssh-check"
)

func getAnnotationInt32(vmfr *restorev1alpha1.VirtualMachineFileRestore, key string) int32 {
	v, ok := vmfr.Annotations[key]
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

func setAnnotationInt32(vmfr *restorev1alpha1.VirtualMachineFileRestore, key string, val int32) {
	if vmfr.Annotations == nil {
		vmfr.Annotations = make(map[string]string)
	}
	vmfr.Annotations[key] = strconv.FormatInt(int64(val), 10)
}

func getAnnotationTime(vmfr *restorev1alpha1.VirtualMachineFileRestore, key string) *time.Time {
	v, ok := vmfr.Annotations[key]
	if !ok {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.After(time.Now()) {
		return nil
	}
	return &t
}

func setAnnotationTime(vmfr *restorev1alpha1.VirtualMachineFileRestore, key string, t time.Time) {
	if vmfr.Annotations == nil {
		vmfr.Annotations = make(map[string]string)
	}
	vmfr.Annotations[key] = t.UTC().Format(time.RFC3339Nano)
}
