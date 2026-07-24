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

package e2e

import (
	"fmt"

	linuxhelpers "kubevirt.io/vm-file-restore-operator/guest-helpers/linux"
)

const stagedHelperPath = "/tmp/filerestore-operator-helper.sh"

// installGuestHelper stages the embedded filerestore.sh on the VM, then runs
// the embedded setup.sh (which prefers the staged helper over downloading).
// This works for standalone QE binaries that have no git checkout on disk.
func installGuestHelper(vmiName, namespace, operatorPubKey, identityFile string) error {
	helperScript, err := linuxhelpers.FileRestoreScript()
	if err != nil {
		return fmt.Errorf("read embedded filerestore.sh: %w", err)
	}
	setupScript, err := linuxhelpers.SetupScript()
	if err != nil {
		return fmt.Errorf("read embedded setup.sh: %w", err)
	}

	stageCmd := fmt.Sprintf(
		"cat <<'HELPER_EOF' > %s\n%s\nHELPER_EOF\nchmod 0644 %s",
		stagedHelperPath, helperScript, stagedHelperPath,
	)
	if _, err := runSSHCommand(vmiName, namespace, stageCmd, identityFile); err != nil {
		return fmt.Errorf("stage filerestore.sh on VM: %w", err)
	}

	setupCmd := fmt.Sprintf(
		"cat <<'SETUP_EOF' | bash -s -- %s\n%s\nSETUP_EOF",
		shellEscape(operatorPubKey), setupScript,
	)
	if _, err := runSSHCommand(vmiName, namespace, setupCmd, identityFile); err != nil {
		return fmt.Errorf("run guest setup.sh: %w", err)
	}
	return nil
}
