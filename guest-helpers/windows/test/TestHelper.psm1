# TestHelper.psm1 - Shared helpers for filerestore.bat Pester tests.

$BatFile = Join-Path $PSScriptRoot '..' 'filerestore.bat'

function Get-TestableScript {
    param([string]$OutputPath)

    $content = Get-Content -Path $BatFile -Raw

    # Strip the bat stub (lines 1-9): everything before the closing #> comment
    $content = $content -replace '(?s)^.*?#>\r?\n', ''

    # Replace 'exit N' (literal integer) with throw
    $content = $content -replace '\bexit\s+(\d+)', 'throw "EXIT:$1"'
    # Replace 'exit $variable' with throw (e.g., exit $LASTEXITCODE)
    $content = $content -replace '\bexit\s+\$(\w+)', 'throw "EXIT:$($$$1)"'

    # Strip wrapper function definitions so Pester can mock the global stubs instead
    $content = $content -replace '(?m)^function New-Junction \{[^}]+\}\s*$', ''
    $content = $content -replace '(?m)^function Remove-Junction \{[^}]+\}\s*$', ''
    $content = $content -replace '(?m)^function Invoke-Robocopy \{[^}]+\}\s*$', ''
    $content = $content -replace '(?m)^function Read-FileContent \{[^}]+\}\s*$', ''

    Set-Content -Path $OutputPath -Value $content -NoNewline
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
    # Script wrapper functions (defined in filerestore.bat, need stubs for Pester mocking)
    function global:New-Junction { [CmdletBinding()] param([string]$Path, [string]$Target) }
    function global:Remove-Junction { [CmdletBinding()] param([string]$Path) }
    function global:Invoke-Robocopy { [CmdletBinding()] param([string[]]$Arguments) return 0 }
    function global:Read-FileContent { [CmdletBinding()] param([string]$Path) return '' }
}

Export-ModuleMember -Function Get-TestableScript, Initialize-WindowsCmdletStubs
