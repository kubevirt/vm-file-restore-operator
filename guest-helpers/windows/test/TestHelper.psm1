# TestHelper.psm1 - Shared helpers for filerestore.bat Pester tests.

$BatFile = Join-Path $PSScriptRoot '..' 'filerestore.bat'

function Get-TestableScript {
    param([string]$OutputPath)

    $lines = Get-Content -Path $BatFile
    # Strip the batch polyglot preamble (everything up to the closing #>)
    $preambleEnd = -1
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match '#>\s*$') {
            $preambleEnd = $i
            break
        }
    }
    if ($preambleEnd -lt 0) {
        Copy-Item -Path $BatFile -Destination $OutputPath
    } else {
        $lines[($preambleEnd + 1)..($lines.Count - 1)] | Set-Content -Path $OutputPath
    }
    return $OutputPath
}

function Initialize-WindowsCmdletStubs {
    # Storage cmdlets — parameter declarations are required so Pester can bind
    # named arguments and ParameterFilter assertions work correctly.
    if (-not (Get-Command Get-Disk -ErrorAction SilentlyContinue)) {
        function global:Get-Disk { [CmdletBinding()] param() }
    }
    if (-not (Get-Command Set-Disk -ErrorAction SilentlyContinue)) {
        function global:Set-Disk {
            [CmdletBinding()]
            param([int]$Number, [System.Nullable[bool]]$IsOffline, [System.Nullable[bool]]$IsReadOnly)
        }
    }
    if (-not (Get-Command Get-Partition -ErrorAction SilentlyContinue)) {
        function global:Get-Partition {
            [CmdletBinding()]
            param([int]$DiskNumber, [int]$PartitionNumber, [char]$DriveLetter)
        }
    }
    if (-not (Get-Command Add-PartitionAccessPath -ErrorAction SilentlyContinue)) {
        function global:Add-PartitionAccessPath {
            [CmdletBinding()]
            param([int]$DiskNumber, [int]$PartitionNumber, [switch]$AssignDriveLetter)
        }
    }
    if (-not (Get-Command Unlock-BitLocker -ErrorAction SilentlyContinue)) {
        function global:Unlock-BitLocker {
            [CmdletBinding()]
            param([string]$MountPoint, [string]$RecoveryPassword)
        }
    }
}

Export-ModuleMember -Function Get-TestableScript, Initialize-WindowsCmdletStubs
