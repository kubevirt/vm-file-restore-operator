# Pester tests for guest-helpers/windows/filerestore.bat

BeforeAll {
    Import-Module "$PSScriptRoot/TestHelper.psm1" -Force
    Initialize-WindowsCmdletStubs

    $script:TestScript = Join-Path $TestDrive 'filerestore.ps1'
    Get-TestableScript -OutputPath $script:TestScript

    # Helper: dot-source only the functions (test mode)
    # The test-mode guard uses 'return' (unchanged by exit→throw patching)
    $script:FuncScript = Join-Path $TestDrive 'filerestore_funcs.ps1'
    $content = Get-Content -Path $script:TestScript -Raw
    $content = $content -replace "if \(\`$env:FILERESTORE_TEST_MODE -eq '1'\) \{ return \}", 'return'
    Set-Content -Path $script:FuncScript -Value $content -NoNewline

    # Helper to run the script with arguments and capture exit code
    function script:Invoke-TestScript {
        param(
            [string[]]$Arguments = @(),
            [hashtable]$Env = @{}
        )
        try {
            foreach ($key in $Env.Keys) {
                Set-Item "env:$key" $Env[$key]
            }
            . $script:TestScript @Arguments
            return @{ ExitCode = 0; Threw = $false }
        } catch {
            if ($_.Exception.Message -match '^EXIT:(.+)$') {
                return @{ ExitCode = [int]$Matches[1]; Threw = $true }
            }
            throw
        } finally {
            foreach ($key in $Env.Keys) {
                Remove-Item "env:$key" -ErrorAction SilentlyContinue
            }
        }
    }
}

# =============================================================================
# SSH_ORIGINAL_COMMAND validation
# =============================================================================
Describe 'SSH command restriction' {
    It 'rejects disallowed command' {
        $env:SSH_ORIGINAL_COMMAND = 'C:\evil.exe restore'
        try {
            { . $script:TestScript } | Should -Throw 'EXIT:1'
        } finally {
            Remove-Item env:SSH_ORIGINAL_COMMAND -ErrorAction SilentlyContinue
        }
    }

    It 'rejects command with partial path match' {
        $env:SSH_ORIGINAL_COMMAND = 'C:\Program Files\other\filerestore.bat'
        try {
            { . $script:TestScript } | Should -Throw 'EXIT:1'
        } finally {
            Remove-Item env:SSH_ORIGINAL_COMMAND -ErrorAction SilentlyContinue
        }
    }

    It 'passes through when SSH_ORIGINAL_COMMAND is not set' {
        Remove-Item env:SSH_ORIGINAL_COMMAND -ErrorAction SilentlyContinue
        { . $script:TestScript } | Should -Throw 'EXIT:1'
    }
}

# =============================================================================
# Argument parsing
# =============================================================================
Describe 'Argument parsing' {
    It 'no arguments shows usage and exits 1' {
        { . $script:TestScript } | Should -Throw 'EXIT:1'
    }

    It 'unknown mode shows error' {
        $result = Invoke-TestScript -Arguments @('bogus')
        $result.ExitCode | Should -Be 1
    }

    It 'missing --mount-path exits 1' {
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC')
        $result.ExitCode | Should -Be 1
    }

    It 'missing --serial in restore mode exits 1' {
        Mock Get-Disk { @() }
        $result = Invoke-TestScript -Arguments @('restore', '--mount-path', '/tmp/mnt')
        $result.ExitCode | Should -Be 1
    }

    It 'unknown flag rejected' {
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC', '--mount-path', '/tmp/mnt', '--bogus', 'val')
        $result.ExitCode | Should -Be 1
    }

    It 'rejects unknown mode' {
        $result = Invoke-TestScript -Arguments @('bogus')
        $result.ExitCode | Should -Be 1
    }

    It 'rejects delete as unknown mode' {
        $result = Invoke-TestScript -Arguments @('delete', '--mount-path', '/tmp/mnt')
        $result.ExitCode | Should -Be 1
    }

    It 'cleanup without --mount-path exits 1' {
        $result = Invoke-TestScript -Arguments @('cleanup')
        $result.ExitCode | Should -Be 1
    }
}

# =============================================================================
# Cleanup mode
# =============================================================================
Describe 'Cleanup mode' {
    BeforeEach {
        Mock Test-Path { $false }
        Mock Remove-Junction { }
        Mock Set-Disk { }
        Mock Get-Item { }
        Mock Get-Partition { }
    }

    It 'completes successfully with valid --mount-path' {
        $result = Invoke-TestScript -Arguments @('cleanup', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
    }

    It 'removes junction when mount path exists' {
        Mock Test-Path { $true }

        $result = Invoke-TestScript -Arguments @('cleanup', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
        Should -Invoke Remove-Junction -Times 1
    }
}

# =============================================================================
# Disk discovery
# =============================================================================
Describe 'Disk discovery' {
    BeforeEach {
        Mock Set-Disk { }
        Mock Get-Partition { }
        Mock Add-PartitionAccessPath { }
        Mock New-Junction { }
        Mock Remove-Junction { }
        Mock Test-Path { $false }
        Mock Unlock-BitLocker { }
    }

    It 'finds disk by serial number with whitespace trim' {
        Mock Get-Disk {
            @([PSCustomObject]@{
                Number = 1
                SerialNumber = '  ABC123  '
                IsOffline = $false
                IsReadOnly = $false
            })
        }
        Mock Get-Partition {
            [PSCustomObject]@{
                DiskNumber = 1; PartitionNumber = 1
                Type = 'Basic'; Size = 1GB; DriveLetter = 'E'
            }
        }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
    }

    It 'exits 1 when disk not found' {
        Mock Get-Disk { @() }
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'NOTFOUND', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 1
    }

    It 'brings offline disks online' {
        Mock Get-Disk {
            @(
                [PSCustomObject]@{ Number = 0; SerialNumber = 'OTHER'; IsOffline = $true; IsReadOnly = $false },
                [PSCustomObject]@{ Number = 1; SerialNumber = 'ABC123'; IsOffline = $false; IsReadOnly = $false }
            )
        }
        Mock Get-Partition {
            [PSCustomObject]@{
                DiskNumber = 1; PartitionNumber = 1
                Type = 'Basic'; Size = 1GB; DriveLetter = 'E'
            }
        }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
        Should -Invoke Set-Disk -Times 1 -Exactly -ParameterFilter { $IsReadOnly -eq $false }
    }
}

# =============================================================================
# Partition and drive letter
# =============================================================================
Describe 'Partition discovery' {
    BeforeEach {
        Mock Set-Disk { }
        Mock New-Junction { }
        Mock Remove-Junction { }
        Mock Unlock-BitLocker { }
        Mock Get-Disk {
            @([PSCustomObject]@{
                Number = 1; SerialNumber = 'ABC123'
                IsOffline = $false; IsReadOnly = $false
            })
        }
    }

    It 'filters out Reserved/System/Recovery partitions' {
        Mock Get-Partition {
            @(
                [PSCustomObject]@{ DiskNumber = 1; PartitionNumber = 1; Type = 'Reserved'; Size = 100MB; DriveLetter = $null },
                [PSCustomObject]@{ DiskNumber = 1; PartitionNumber = 2; Type = 'System'; Size = 500MB; DriveLetter = $null },
                [PSCustomObject]@{ DiskNumber = 1; PartitionNumber = 3; Type = 'Basic'; Size = 50GB; DriveLetter = 'E' }
            )
        }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
    }

    It 'exits 1 when no usable partition found' {
        Mock Get-Partition {
            @(
                [PSCustomObject]@{ DiskNumber = 1; PartitionNumber = 1; Type = 'Reserved'; Size = 100MB; DriveLetter = $null }
            )
        }
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 1
    }

    It 'assigns drive letter when missing' {
        Mock Get-Partition {
            [PSCustomObject]@{ DiskNumber = 1; PartitionNumber = 2; Type = 'Basic'; Size = 50GB; DriveLetter = $null }
        } -ParameterFilter { -not $PartitionNumber }
        Mock Get-Partition {
            [PSCustomObject]@{ DiskNumber = 1; PartitionNumber = 2; Type = 'Basic'; Size = 50GB; DriveLetter = 'F' }
        } -ParameterFilter { $PartitionNumber -gt 0 }
        Mock Add-PartitionAccessPath { }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
        Should -Invoke Add-PartitionAccessPath -Times 1
    }
}

# =============================================================================
# BitLocker
# =============================================================================
Describe 'BitLocker handling' {
    BeforeEach {
        Mock Set-Disk { }
        Mock New-Junction { }
        Mock Remove-Junction { }
        Mock Get-Disk {
            @([PSCustomObject]@{
                Number = 1; SerialNumber = 'ABC123'
                IsOffline = $false; IsReadOnly = $false
            })
        }
        Mock Get-Partition {
            [PSCustomObject]@{
                DiskNumber = 1; PartitionNumber = 1
                Type = 'Basic'; Size = 50GB; DriveLetter = 'E'
            }
        }
    }

    It 'unlocks BitLocker when lockfile exists' {
        Mock Test-Path { $true }
        Mock Read-FileContent { 'recovery-pass-123' }
        Mock Unlock-BitLocker { }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
        Should -Invoke Unlock-BitLocker -Times 1
    }

    It 'skips BitLocker when lockfile missing' {
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }
        Mock Unlock-BitLocker { }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
        Should -Invoke Unlock-BitLocker -Times 0
    }
}

# =============================================================================
# Junction creation
# =============================================================================
Describe 'Junction creation' {
    BeforeEach {
        Mock Set-Disk { }
        Mock Remove-Junction { }
        Mock Unlock-BitLocker { }
        Mock Get-Disk {
            @([PSCustomObject]@{
                Number = 1; SerialNumber = 'ABC123'
                IsOffline = $false; IsReadOnly = $false
            })
        }
        Mock Get-Partition {
            [PSCustomObject]@{
                DiskNumber = 1; PartitionNumber = 1
                Type = 'Basic'; Size = 50GB; DriveLetter = 'E'
            }
        }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }
    }

    It 'removes existing junction before creating new one' {
        Mock Test-Path { $true } -ParameterFilter { $Path -eq '/tmp/backup' }
        Mock New-Junction { }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
        Should -Invoke Remove-Junction -Times 1
        Should -Invoke New-Junction -Times 1
    }

    It 'exits 1 when junction creation fails' {
        Mock Test-Path { $false } -ParameterFilter { $Path -eq '/tmp/backup' }
        Mock New-Junction { }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 1
    }
}

# =============================================================================
# Manual vs automatic mode
# =============================================================================
Describe 'Manual vs automatic mode' {
    BeforeEach {
        Mock Set-Disk { }
        Mock New-Junction { }
        Mock Remove-Junction { }
        Mock Unlock-BitLocker { }
        Mock Invoke-Robocopy { 0 }
        Mock New-Item { }
        Mock Get-Disk {
            @([PSCustomObject]@{
                Number = 1; SerialNumber = 'ABC123'
                IsOffline = $false; IsReadOnly = $false
            })
        }
        Mock Get-Partition {
            [PSCustomObject]@{
                DiskNumber = 1; PartitionNumber = 1
                Type = 'Basic'; Size = 50GB; DriveLetter = 'E'
            }
        }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
    }

    It 'manual mode exits 0 after junction (no --source-path)' {
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
    }

    It 'automatic mode: source path not found exits 1' {
        Mock Test-Path {
            if ($Path -like '*testdata*') { return $false }
            return $true
        } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata')
        $result.ExitCode | Should -Be 1
    }

    It 'automatic mode: robocopy success (exit 0-7) exits 0' {
        Mock Invoke-Robocopy { 1 }
        Mock Join-Path { "$Path/$ChildPath" }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata')
        $result.ExitCode | Should -Be 0
    }

    It 'automatic mode: robocopy exit 7 is still success' {
        Mock Invoke-Robocopy { 7 }
        Mock Join-Path { "$Path/$ChildPath" }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata')
        $result.ExitCode | Should -Be 0
    }

    It 'automatic mode: robocopy failure (exit 8+) exits 1' {
        Mock Invoke-Robocopy { 8 }
        Mock Join-Path { "$Path/$ChildPath" }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata')
        $result.ExitCode | Should -Be 1
    }
}

# =============================================================================
# Drive letter stripping
# =============================================================================
Describe 'Drive letter stripping logic' {
    It 'strips drive letter from C:\test' {
        $result = 'C:\test' -replace '^[A-Za-z]:\\', ''
        $result | Should -Be 'test'
    }

    It 'strips lowercase drive letter from d:\folder\file.txt' {
        $result = 'd:\folder\file.txt' -replace '^[A-Za-z]:\\', ''
        $result | Should -Be 'folder\file.txt'
    }

    It 'preserves path without drive letter' {
        $result = '\test\data' -replace '^[A-Za-z]:\\', ''
        $result | Should -Be '\test\data'
    }

    It 'preserves UNC path' {
        $result = '\\server\share' -replace '^[A-Za-z]:\\', ''
        $result | Should -Be '\\server\share'
    }
}

# =============================================================================
# Function: Unmount-AndCleanup (unit tests via dot-source)
# =============================================================================
Describe 'Unmount-AndCleanup function' {
    BeforeAll {
        . $script:FuncScript
    }

    BeforeEach {
        Mock Test-Path { $false }
        Mock Remove-Junction { }
        Mock Set-Disk { }
        Mock Get-Item { }
        Mock Get-Partition { }
    }

    It 'sets disk offline when DiskNumber provided' {
        Unmount-AndCleanup -MountPath '/tmp/backup' -DiskNumber 3
        Should -Invoke Set-Disk -Times 1
    }

    It 'does not set disk offline when DiskNumber is -1 and path does not exist' {
        Unmount-AndCleanup -MountPath '/tmp/nonexistent'
        Should -Invoke Set-Disk -Times 0
    }

    It 'calls Remove-Junction when path exists' {
        Mock Test-Path { $true }

        Unmount-AndCleanup -MountPath '/tmp/backup' -DiskNumber 1
        Should -Invoke Remove-Junction -Times 1
    }
}
