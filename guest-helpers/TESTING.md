# Guest Helper Script Tests

Unit tests for the Linux (`filerestore.sh`) and Windows (`filerestore.bat`) guest helper scripts.

## Quick Start

Run all script tests (requires Docker or Podman):

```bash
make test-scripts
```

Run individually:

```bash
make test-scripts-linux      # BATS tests for filerestore.sh
make test-scripts-windows    # Pester tests for filerestore.bat
```

By default, `CONTAINER_ENGINE` inherits from `CONTAINER_TOOL`, which defaults to `docker`. To override:

```bash
CONTAINER_ENGINE=docker make test-scripts
```

## Prerequisites

Only a container runtime (Docker or Podman) is needed:

- **Linux tests**: [`bats/bats`](https://hub.docker.com/r/bats/bats) official image (includes [bats-core](https://github.com/bats-core/bats-core), [bats-support](https://github.com/bats-core/bats-support), [bats-assert](https://github.com/bats-core/bats-assert))
- **Windows tests**: [`mcr.microsoft.com/dotnet/sdk`](https://learn.microsoft.com/en-us/powershell/scripting/install/powershell-in-docker) with [Pester v5](https://pester.dev/) installed at runtime (pinned version)

No local installation of BATS, PowerShell, or Pester is required.

## Test Structure

```
guest-helpers/
  linux/
    filerestore.sh
    test/
      filerestore.bats          # BATS test cases
      test_helper.bash          # Shared setup, mock defaults
      mocks/                    # Stub scripts for system commands
        lsblk, blkid, mount, umount, rsync, sync, sudo
  windows/
    filerestore.bat
    test/
      filerestore.Tests.ps1     # Pester test cases
      TestHelper.psm1           # Script extraction, Windows cmdlet stubs
```

## How the Tests Work

### Linux (BATS)

The test suite uses mock scripts that shadow real system commands via `$PATH`
prepending. Each mock reads environment variables to control its behavior
(output, exit codes) and logs calls to `$MOCK_CALL_LOG` for verification.

The production script is modified minimally for testability:
- `FILERESTORE_SKIP_ROOT_CHECK=1` bypasses the `sudo` re-exec
- `FILERESTORE_SOURCED=1` allows sourcing functions without running main logic

### Windows (Pester)

The test helper extracts the PowerShell portion from the bat/PS polyglot file
by stripping the batch preamble (everything up to the closing `#>` block
comment marker). The extracted script is dot-sourced, which triggers a
`$MyInvocation.InvocationName` guard that loads all function definitions
without executing the main entry point.

Windows-only cmdlets (`Get-Disk`, `Set-Disk`, `Get-Partition`, etc.) are
defined as stub functions with proper parameter declarations, enabling Pester's
`Mock` and `Should -Invoke` to work on Linux.

## Shellcheck

Run static analysis on bash scripts:

```bash
# Requires shellcheck installed locally
make shellcheck

# Or via container
docker run --rm -v $(pwd):/workspace:Z -w /workspace koalaman/shellcheck:stable \
  guest-helpers/linux/filerestore.sh guest-helpers/linux/setup.sh
```
