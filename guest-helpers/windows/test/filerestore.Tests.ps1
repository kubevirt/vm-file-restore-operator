# Pester tests for guest-helpers/windows/filerestore.bat

BeforeAll {
    Import-Module "$PSScriptRoot/TestHelper.psm1" -Force
    Initialize-WindowsCmdletStubs

    $script:TestScript = Join-Path $TestDrive 'filerestore.ps1'
    Get-TestableScript -OutputPath $script:TestScript

    # Dot-source to load all functions (dot-source guard returns before main logic)
    . $script:TestScript

    # Generate realistic robocopy summary output with fixed-width columns matching real robocopy
    function script:New-RobocopyOutput {
        param([int]$Total = 3, [int]$Copied = 2, [int]$Skipped = 1, [int]$Failed = 0)
        @(
            "",
            "               Total    Copied   Skipped  Mismatch    FAILED    Extras",
            "    Dirs :         1         0         1         0         0         0",
            ("   Files :{0,10}{1,10}{2,10}         0{3,10}         0" -f $Total, $Copied, $Skipped, $Failed),
            "   Bytes :       100        50        50         0         0         0",
            "   Times :   0:00:00   0:00:00                       0:00:00   0:00:00"
        )
    }

    # Shared mock baseline for restore-mode tests: standard disk, partition,
    # and no-op stubs for disk/junction/BitLocker cmdlets.
    function script:Initialize-RestoreMocks {
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
        Mock Get-Partition {
            [PSCustomObject]@{
                DiskNumber = 1; PartitionNumber = 1
                Type = 'Basic'; Size = 50GB; DriveLetter = 'E'
            }
        }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
    }

    # Helper to run the main entry point and capture exit code
    function script:Invoke-TestScript {
        param(
            [string[]]$Arguments = @(),
            [hashtable]$Env = @{}
        )
        $savedEnv = @{}
        try {
            foreach ($key in $Env.Keys) {
                if (Test-Path "env:$key") { $savedEnv[$key] = (Get-Item "env:$key").Value }
                Set-Item "env:$key" $Env[$key]
            }
            return @{ ExitCode = (Invoke-FileRestore $Arguments) }
        } catch {
            throw "Invoke-FileRestore threw unexpectedly: $_"
        } finally {
            foreach ($key in $Env.Keys) {
                if ($savedEnv.ContainsKey($key)) { Set-Item "env:$key" $savedEnv[$key] }
                else { Remove-Item "env:$key" -ErrorAction SilentlyContinue }
            }
        }
    }
}

# =============================================================================
# SSH_ORIGINAL_COMMAND validation
# =============================================================================
Describe 'SSH command restriction' {
    AfterEach {
        Remove-Item env:SSH_ORIGINAL_COMMAND -ErrorAction SilentlyContinue
    }

    It 'rejects disallowed command' {
        $env:SSH_ORIGINAL_COMMAND = 'C:\evil.exe restore'
        Test-SshCommand | Should -Be 'rejected'
    }

    It 'rejects command with partial path match' {
        $env:SSH_ORIGINAL_COMMAND = 'C:\Program Files\other\filerestore.bat'
        Test-SshCommand | Should -Be 'rejected'
    }

    It 'returns null when SSH_ORIGINAL_COMMAND is not set' {
        Remove-Item env:SSH_ORIGINAL_COMMAND -ErrorAction SilentlyContinue
        Test-SshCommand | Should -BeNullOrEmpty
    }

    It 'extracts arguments from valid command' {
        $env:SSH_ORIGINAL_COMMAND = '"C:\Program Files\filerestore\filerestore.bat" restore --serial ABC'
        Test-SshCommand | Should -Be 'restore --serial ABC'
    }

    It 'rejects command with wrong script name' {
        $env:SSH_ORIGINAL_COMMAND = '"C:\Program Files\filerestore\filerestore.bat-evil" restore'
        Test-SshCommand | Should -Be 'rejected'
    }
}

# =============================================================================
# Argument parsing
# =============================================================================
Describe 'Argument parsing' {
    It 'no arguments shows usage and exits 1' {
        $result = Invoke-TestScript -Arguments @()
        $result.ExitCode | Should -Be 1
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

    It '--serial as last arg (no value) exits 1' {
        $result = Invoke-TestScript -Arguments @('restore', '--mount-path', '/tmp/mnt', '--serial')
        $result.ExitCode | Should -Be 1
    }

    It '--mount-path as last arg (no value) exits 1' {
        $result = Invoke-TestScript -Arguments @('restore', '--mount-path')
        $result.ExitCode | Should -Be 1
    }

    It 'unknown flag rejected' {
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC', '--mount-path', '/tmp/mnt', '--bogus', 'val')
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
        Initialize-RestoreMocks
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
        Initialize-RestoreMocks
    }

    It 'unlocks BitLocker when lockfile exists' {
        Mock Test-Path { $true } -ParameterFilter { $Path -like '*lockfile*' }
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
        Initialize-RestoreMocks
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
        Initialize-RestoreMocks
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 0 -Copied 0 -Skipped 0; $script:RobocopyExitCode = 0 }
        Mock New-Item { }
    }

    It 'manual mode exits 0 after junction (no --source-path)' {
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup')
        $result.ExitCode | Should -Be 0
    }

    It 'automatic mode: UNC source path rejected' {
        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '\\server\share\data')
        $result.ExitCode | Should -Be 1
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
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 1 -Copied 1 -Skipped 0; $script:RobocopyExitCode = 1 }
        Mock Join-Path { "$Path/$ChildPath" }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata')
        $result.ExitCode | Should -Be 0
    }

    It 'automatic mode: robocopy success emits file count from summary' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 5 -Copied 3 -Skipped 2; $script:RobocopyExitCode = 1 }
        Mock Join-Path { "$Path/$ChildPath" }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' -and $PathType -ne 'Leaf' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $PathType -eq 'Leaf' }

        $output = Invoke-FileRestore @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata') *>&1
        $countLine = $output | Where-Object { $_ -match '\[filerestore\] \d+ files restored' }
        $countLine | Should -Not -BeNullOrEmpty
        $countLine | Should -BeLike '*3 files restored*'
    }

    It 'automatic mode: single file restore emits 1 files restored' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 1 -Copied 1 -Skipped 0; $script:RobocopyExitCode = 1 }
        Mock Join-Path { "$Path/$ChildPath" }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }

        $output = Invoke-FileRestore @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', 'C:\test\file.txt') *>&1
        $countLine = $output | Where-Object { $_ -match '\[filerestore\] \d+ files restored' }
        $countLine | Should -Not -BeNullOrEmpty
        $countLine | Should -BeLike '*1 files restored*'
    }

    It 'automatic mode: robocopy exit 0 emits 0 files restored' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 3 -Copied 0 -Skipped 3; $script:RobocopyExitCode = 0 }
        Mock Join-Path { "$Path/$ChildPath" }

        $output = Invoke-FileRestore @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata') *>&1
        $countLine = $output | Where-Object { $_ -match '\[filerestore\] \d+ files restored' }
        $countLine | Should -Not -BeNullOrEmpty
        $countLine | Should -BeLike '*0 files restored*'
    }

    It 'automatic mode: robocopy exit 7 is still success' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 5 -Copied 2 -Skipped 3; $script:RobocopyExitCode = 7 }
        Mock Join-Path { "$Path/$ChildPath" }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata')
        $result.ExitCode | Should -Be 0
    }

    It 'automatic mode: robocopy failure (exit 8+) exits 1' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 3 -Copied 0 -Failed 3; $script:RobocopyExitCode = 8 }
        Mock Join-Path { "$Path/$ChildPath" }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata')
        $result.ExitCode | Should -Be 1
    }

    It 'automatic mode: file at drive root does not fail on New-Item' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 1 -Copied 1 -Skipped 0; $script:RobocopyExitCode = 1 }
        Mock Join-Path { "$Path/$ChildPath" }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', 'E:\file.txt')
        $result.ExitCode | Should -Be 0
        Should -Invoke New-Item -Times 0
    }

    It 'automatic mode: robocopy exit 3 reports copied count from summary' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 5 -Copied 2 -Skipped 3; $script:RobocopyExitCode = 3 }
        Mock Join-Path { "$Path/$ChildPath" }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' -and $PathType -ne 'Leaf' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $PathType -eq 'Leaf' }

        $output = Invoke-FileRestore @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', '/tmp/testdata') *>&1
        $countLine = $output | Where-Object { $_ -match '\[filerestore\] \d+ files restored' }
        $countLine | Should -Not -BeNullOrEmpty
        $countLine | Should -BeLike '*2 files restored*'
    }

    It 'automatic mode: trailing backslash on --source-path succeeds' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 0 -Copied 0 -Skipped 0; $script:RobocopyExitCode = 0 }
        Mock Join-Path { "$Path/$ChildPath" }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', 'C:\test\')
        $result.ExitCode | Should -Be 0
        Should -Invoke Invoke-Robocopy -Times 1
    }

    It 'automatic mode: trailing backslash is treated as directory restore' {
        Mock Invoke-Robocopy { $script:RobocopyOutput = New-RobocopyOutput -Total 0 -Copied 0 -Skipped 0; $script:RobocopyExitCode = 0 }
        Mock Join-Path { "$Path/$ChildPath" }
        Mock Test-Path { $true } -ParameterFilter { $Path -and $Path -notlike '*lockfile*' -and $PathType -ne 'Leaf' }
        Mock Test-Path { $false } -ParameterFilter { $Path -like '*lockfile*' }
        Mock Test-Path { $false } -ParameterFilter { $PathType -eq 'Leaf' }

        $result = Invoke-TestScript -Arguments @('restore', '--serial', 'ABC123', '--mount-path', '/tmp/backup', '--source-path', 'C:\test\')
        $result.ExitCode | Should -Be 0
        Should -Invoke Invoke-Robocopy -Times 1
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

    It 'normalizes leading backslash when no drive letter is present' {
        $result = ('\test\data' -replace '^[A-Za-z]:\\', '').TrimStart('\\')
        $result | Should -Be 'test\data'
    }

    It 'preserves UNC path' {
        $result = '\\server\share' -replace '^[A-Za-z]:\\', ''
        $result | Should -Be '\\server\share'
    }

    It 'strips drive letter from path with trailing backslash C:\test\' {
        $result = 'C:\test\' -replace '^[A-Za-z]:\\', ''
        $result | Should -Be 'test\'
    }

    It 'strips drive letter from root path C:\' {
        $result = 'C:\' -replace '^[A-Za-z]:\\', ''
        $result | Should -Be ''
    }
}

# =============================================================================
# Function: Unmount-AndCleanup (unit tests via dot-source)
# =============================================================================
Describe 'Unmount-AndCleanup function' {
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

# =============================================================================
# Parse-RobocopyCopiedCount
# =============================================================================
Describe 'Parse-RobocopyCopiedCount' {
    It 'extracts copied count from standard summary' {
        $output = New-RobocopyOutput -Total 10 -Copied 7 -Skipped 3
        Parse-RobocopyCopiedCount -Output $output | Should -Be 7
    }

    It 'returns 0 when no files copied' {
        $output = New-RobocopyOutput -Total 5 -Copied 0 -Skipped 5
        Parse-RobocopyCopiedCount -Output $output | Should -Be 0
    }

    It 'returns 0 for empty output' {
        Parse-RobocopyCopiedCount -Output @() | Should -Be 0
    }

    It 'returns 0 for null output' {
        Parse-RobocopyCopiedCount -Output $null | Should -Be 0
    }

    It 'returns 0 when no Files line present' {
        $output = @("some random output", "no summary here")
        Parse-RobocopyCopiedCount -Output $output | Should -Be 0
    }

    It 'handles large file counts' {
        $output = @(
            "               Total    Copied   Skipped  Mismatch    FAILED    Extras",
            "   Files :     12345      9876      2469         0         0         0"
        )
        Parse-RobocopyCopiedCount -Output $output | Should -Be 9876
    }

    It 'handles single file copied' {
        $output = New-RobocopyOutput -Total 1 -Copied 1 -Skipped 0
        Parse-RobocopyCopiedCount -Output $output | Should -Be 1
    }

    It 'ignores Dirs line' {
        $output = @(
            "    Dirs :         5         3         2         0         0         0"
        )
        Parse-RobocopyCopiedCount -Output $output | Should -Be 0
    }
}
