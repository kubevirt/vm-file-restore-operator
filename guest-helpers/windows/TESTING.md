# Guest Helper Script Tests

Unit tests for the Windows guest helper script (`filerestore.bat`).

## Quick Start

Run script tests (requires Docker or Podman):

```bash
make test-scripts-windows
```

By default, `CONTAINER_ENGINE` inherits from `CONTAINER_TOOL`, which defaults to `docker`. To override:

```bash
CONTAINER_ENGINE=docker make test-scripts-windows
```

## Prerequisites

Only a container runtime (Docker or Podman) is needed:

- [`mcr.microsoft.com/dotnet/sdk`](https://learn.microsoft.com/en-us/powershell/scripting/install/powershell-in-docker) with [Pester v5](https://pester.dev/) installed at runtime (pinned version; requires network access to the PowerShell Gallery)

No local installation of PowerShell or Pester is required (the container downloads Pester on demand).

## Test Structure

```
guest-helpers/windows/
  filerestore.bat
  test/
    filerestore.Tests.ps1     # Pester test cases
    TestHelper.psm1           # Script extraction, Windows cmdlet stubs
```

## How the Tests Work

The test helper extracts the PowerShell portion from the bat/PS polyglot file
by stripping the batch preamble (everything up to the closing `#>` block
comment marker). The extracted script is dot-sourced, which triggers a
`$MyInvocation.InvocationName` guard that loads all function definitions
without executing the main entry point.

Windows-only cmdlets (`Get-Disk`, `Set-Disk`, `Get-Partition`, etc.) are
defined as stub functions with proper parameter declarations, enabling Pester's
`Mock` and `Should -Invoke` to work on Linux.
