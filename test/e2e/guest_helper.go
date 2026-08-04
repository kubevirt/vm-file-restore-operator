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
	"time"

	linuxhelpers "kubevirt.io/vm-file-restore-operator/guest-helpers/linux"
)

// installGuestHelper stages the embedded filerestore.sh under a unique
// root-private directory on the VM, then runs the embedded setup.sh with that
// path so offline / QE installs do not use a shared /tmp location.
func installGuestHelper(vmiName, namespace, operatorPubKey, identityFile string) error {
	helperScript, err := linuxhelpers.FileRestoreScript()
	if err != nil {
		return fmt.Errorf("read embedded filerestore.sh: %w", err)
	}
	setupScript, err := linuxhelpers.SetupScript()
	if err != nil {
		return fmt.Errorf("read embedded setup.sh: %w", err)
	}

	stageDir := fmt.Sprintf("/root/filerestore-helper-%d", time.Now().UnixNano())
	stagedPath := stageDir + "/filerestore.sh"

	// Paths are generated (no user input); heredoc body is literal via HELPER_EOF.
	// chown/chmod run only if cat succeeds (&& after the redirection).
	stageCmd := fmt.Sprintf(
		"mkdir -m 0700 -p %s && cat <<'HELPER_EOF' > %s && chown root:root %s && chmod 0644 %s\n%s\nHELPER_EOF",
		stageDir, stagedPath, stagedPath, stagedPath, helperScript,
	)
	if _, err := runSSHCommand(vmiName, namespace, stageCmd, identityFile); err != nil {
		return fmt.Errorf("stage filerestore.sh on VM: %w", err)
	}
	// Remove staging dir on every exit path after a successful stage (setup may fail).
	// Best-effort: don't mask a setup error with a flaky rm failure.
	defer func() {
		_, _ = runSSHCommand(vmiName, namespace, fmt.Sprintf("rm -rf %s", stageDir), identityFile)
	}()

	setupCmd := fmt.Sprintf(
		"cat <<'SETUP_EOF' | bash -s -- %s %s\n%s\nSETUP_EOF",
		shellEscape(operatorPubKey), shellEscape(stagedPath), setupScript,
	)
	if _, err := runSSHCommand(vmiName, namespace, setupCmd, identityFile); err != nil {
		return fmt.Errorf("run guest setup.sh: %w", err)
	}
	return nil
}
