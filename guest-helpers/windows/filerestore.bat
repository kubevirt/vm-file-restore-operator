# 2>nul & @echo off & goto :BATCH
<#
:BATCH
copy /y "%~f0" "%TEMP%\filerestore.ps1" >nul
powershell -NoProfile -ExecutionPolicy Bypass -File "%TEMP%\filerestore.ps1" %*
set _RC=%ERRORLEVEL%
del /q "%TEMP%\filerestore.ps1" 2>nul
exit /b %_RC%
: #>

# filerestore.bat - Windows guest file-restore helper (bat/PowerShell polyglot).
#
# Usage:
#   Restore (automatic):
#     filerestore.bat restore --serial <SERIAL> --mount-path <PATH> --source-path <PATH>
#
#   Restore (manual - mount only):
#     filerestore.bat restore --serial <SERIAL> --mount-path <PATH>
#
#   Cleanup (unmount and remove mount point):
#     filerestore.bat cleanup --mount-path <PATH>
#
# When --source-path is omitted, the script runs in manual mode (mount only).
# Cleanup is a standalone operation that unmounts and removes the mount point.

$ErrorActionPreference = 'Stop'

# --- Function definitions ---

function Log { param([string]$Message) Write-Host "[filerestore] $Message" }
function Log-Error { param([string]$Message) [Console]::Error.WriteLine("[filerestore] ERROR: $Message") }

function Show-Usage {
    Write-Host "Usage:"
    Write-Host "  filerestore.bat restore --serial <SERIAL> --mount-path <PATH> [--source-path <PATH>]"
    Write-Host "  filerestore.bat cleanup --mount-path <PATH>"
}

# Wrapper functions for native executables (enables Pester mocking)
function New-Junction { param([string]$Path, [string]$Target) cmd /c mklink /J "$Path" "$Target" | Out-Null }
function Remove-Junction { param([string]$Path) cmd /c rmdir "$Path" 2>$null }
function Invoke-Robocopy {
    param([string[]]$Arguments)
    $script:RobocopyOutput = & robocopy @Arguments
    $script:RobocopyOutput | Out-Host
    return $LASTEXITCODE
}
function Read-FileContent { param([string]$Path) return (Get-Content -Path $Path -Raw).Trim() }

# Parse robocopy summary output to extract the "Copied" file count.
# Robocopy emits a summary table like:
#                Total    Copied   Skipped  Mismatch    FAILED    Extras
#    Files :         3         2         1         0         0         0
function Parse-RobocopyCopiedCount {
    param([string[]]$Output)
    foreach ($line in $Output) {
        if ($line -match '^\s*Files\s*:\s+\d+\s+(\d+)') {
            return [int]$Matches[1]
        }
    }
    return 0
}

# Unmount-AndCleanup resolves the junction target to find the disk, removes
# the junction point, and sets the disk offline.
function Unmount-AndCleanup {
    param(
        [Parameter(Mandatory)]
        [string]$MountPath,
        [int]$DiskNumber = -1
    )

    # When DiskNumber is unknown (cleanup mode), resolve it from the junction target
    if ($DiskNumber -lt 0 -and (Test-Path $MountPath)) {
        $item = Get-Item $MountPath -Force -ErrorAction SilentlyContinue
        if ($item -and ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
            # $item.Target may be an array; take the first element as a scalar
            $target = @($item.Target)[0]
            if ($target -and $target -match '^([A-Za-z]):\\') {
                $part = Get-Partition -DriveLetter $Matches[1] -ErrorAction SilentlyContinue
                if ($part) { $DiskNumber = $part.DiskNumber }
            }
        }
    }

    # Remove the junction link (rmdir removes the reparse point without following it)
    if (Test-Path $MountPath) {
        Remove-Junction -Path $MountPath
    }
    if (Test-Path $MountPath) {
        Log "WARNING: Could not remove junction $MountPath"
    }

    # Set disk offline after removing the junction
    if ($DiskNumber -ge 0) {
        Set-Disk -Number $DiskNumber -IsOffline $true -ErrorAction SilentlyContinue
    }
}

# Validate SSH command= restriction and return the extracted arguments or $null.
# Returns 'rejected' if the command is not allowed.
function Test-SshCommand {
    if (-not $env:SSH_ORIGINAL_COMMAND) { return $null }
    if ($env:SSH_ORIGINAL_COMMAND -notmatch '^"?C:\\Program Files\\filerestore\\filerestore\.bat"?(\s|$)') {
        Log-Error "Only filerestore.bat commands are allowed"
        return 'rejected'
    }
    $arguments = $env:SSH_ORIGINAL_COMMAND -replace '^"?C:\\Program Files\\filerestore\\filerestore\.bat"?\s*', ''
    return $arguments
}

function Invoke-FileRestore {
    param([string[]]$Arguments)

    # --- Parse arguments ---
    if ($Arguments.Count -lt 1) { Show-Usage; return 1 }

    $Mode = $Arguments[0]
    if ($Mode -notin @('restore', 'cleanup')) {
        Log-Error "Unknown mode: $Mode"
        Show-Usage; return 1
    }

    $Serial = ''
    $MountPath = ''
    $SourcePath = ''
    $i = 1
    while ($i -lt $Arguments.Count) {
        switch ($Arguments[$i]) {
            '--serial'      {
                if ($i + 1 -ge $Arguments.Count) { Log-Error "--serial requires a value"; return 1 }
                $Serial = $Arguments[$i + 1]; $i += 2
            }
            '--mount-path'  {
                if ($i + 1 -ge $Arguments.Count) { Log-Error "--mount-path requires a value"; return 1 }
                $MountPath = $Arguments[$i + 1]; $i += 2
            }
            '--source-path' {
                if ($i + 1 -ge $Arguments.Count) { Log-Error "--source-path requires a value"; return 1 }
                $SourcePath = $Arguments[$i + 1]; $i += 2
            }
            default {
                Log-Error "Unknown argument: $($Arguments[$i])"
                Show-Usage; return 1
            }
        }
    }

    if (-not $MountPath) {
        Log-Error "--mount-path is required"
        Show-Usage; return 1
    }

    # --- Cleanup mode: unmount and remove mount point ---
    if ($Mode -eq 'cleanup') {
        Unmount-AndCleanup -MountPath $MountPath
        Log "Cleanup of $MountPath completed"
        return 0
    }

    # --- Restore mode requires --serial ---
    if (-not $Serial) {
        Log-Error "--serial is required for $Mode"
        Show-Usage; return 1
    }

    # --- Clear readonly and bring offline disks online ---

    $offLineDisks = Get-Disk | Where-Object IsOffline -eq $True

    foreach ($disk in $offLineDisks) {
        Log "Found Offline Disk: $($disk.Number). Clearing readonly and bringing online..."

        # Clear the ReadOnly attribute at the disk level
        Set-Disk -Number $disk.Number -IsReadOnly $false

        # Bring the disk online
        Set-Disk -Number $disk.Number -IsOffline $false
    }

    # --- Find disk by serial number ---
    $disk = Get-Disk | Where-Object { $_.SerialNumber -and $_.SerialNumber.Trim() -eq $Serial }
    if (-not $disk) {
        Log-Error "Disk with serial $Serial not found"
        return 1
    }
    $diskNumber = $disk.Number
    Log "Found disk: Disk $diskNumber (Serial: $Serial)"

    # --- Bring disk online ---
    if ($disk.IsOffline) {
        Set-Disk -Number $diskNumber -IsOffline $false
    }
    if ($disk.IsReadOnly) {
        Set-Disk -Number $diskNumber -IsReadOnly $false
    }

    # --- Mount: disk already has a formatted partition with restore data ---
    $partition = Get-Partition -DiskNumber $diskNumber -ErrorAction SilentlyContinue |
        Where-Object { $_.Type -notin @('Reserved', 'System', 'Unknown', 'Recovery') -and $_.Size -gt 0 } |
        Select-Object -First 1
    if (-not $partition) {
        Log-Error "No usable partition found on disk $diskNumber"
        return 1
    }

    # Get or assign a drive letter (Windows does not always auto-assign one)
    $driveLetter = $partition.DriveLetter
    if (-not $driveLetter) {
        Add-PartitionAccessPath -DiskNumber $diskNumber -PartitionNumber $partition.PartitionNumber -AssignDriveLetter
        $partition = Get-Partition -DiskNumber $diskNumber -PartitionNumber $partition.PartitionNumber
        $driveLetter = $partition.DriveLetter
        if (-not $driveLetter) {
            Log-Error "Could not assign a drive letter to partition on disk $diskNumber"
            return 1
        }
        Log "Assigned drive letter ${driveLetter}: to partition on disk $diskNumber"
    }

    # --- Unlock BitLocker volume (must use the real drive letter, not a junction) ---
    $BitLockerPasswordFile = "C:\Program Files\filerestore\lockfile.txt"
    if (Test-Path $BitLockerPasswordFile) {
        $recoveryPassword = Read-FileContent -Path $BitLockerPasswordFile
        Log "Unlocking BitLocker volume at ${driveLetter}: ..."
        Unlock-BitLocker -MountPoint "${driveLetter}:" -RecoveryPassword $recoveryPassword
        Log "BitLocker volume unlocked"
    }

    # Create a junction point to redirect $MountPath to the drive
    if (Test-Path $MountPath) { Remove-Junction -Path $MountPath }
    New-Junction -Path $MountPath -Target "${driveLetter}:\"
    if (-not (Test-Path $MountPath)) {
        Log-Error "Failed to create junction from $MountPath to ${driveLetter}:\"
        return 1
    }
    Set-Disk -Number $diskNumber -IsReadOnly $true
    Log "Disk mounted at $MountPath (junction to ${driveLetter}:\)"

    # --- Manual mode: stop here, leave the volume mounted ---
    if (-not $SourcePath) {
        Log "Volume mounted at $MountPath for manual restore operations"
        return 0
    }

    # --- Automatic mode: wrap in try/finally to guarantee cleanup ---
    $copyFailed = $false
    try {
        # Construct relative backup path
        # For full disk snapshots, strip drive letter and leading backslash
        # "C:\test" -> "test", "C:\foo\bar" -> "foo\bar"
        if ($SourcePath -match '^\\\\') {
            Log-Error "UNC paths are not supported for --source-path"
            return 1
        }
        $RelativePath = $SourcePath -replace '^[A-Za-z]:\\', ''
        $RelativePath = $RelativePath.TrimStart('\')
        $BackupPath = Join-Path $MountPath $RelativePath

        # Validate source path on the backup volume (checked after mount)
        if (-not (Test-Path $BackupPath)) {
            Log-Error "Source path $BackupPath does not exist on the backup volume"
            return 1
        }

        # Copy files FROM the backup volume TO the original guest location
        if (Test-Path $BackupPath -PathType Leaf) {
            $srcDir = Split-Path $BackupPath -Parent
            $fileName = Split-Path $BackupPath -Leaf
            $destDir = Split-Path $SourcePath -Parent
            if (-not (Test-Path $destDir)) {
                New-Item -ItemType Directory -Path $destDir -Force | Out-Null
            }
            $rcExit = Invoke-Robocopy -Arguments @("$srcDir", "$destDir", "$fileName", '/COPY:DATS', '/R:1', '/W:1')
        } else {
            if (-not (Test-Path $SourcePath)) {
                New-Item -ItemType Directory -Path $SourcePath -Force | Out-Null
            }
            $rcExit = Invoke-Robocopy -Arguments @("$BackupPath", "$SourcePath", '/E', '/COPY:DATS', '/R:1', '/W:1')
        }

        # robocopy exit codes: 0-7 are success (bits indicate copied/extra/mismatched files), 8+ are errors
        if ($rcExit -ge 8) {
            Log-Error "File copy failed with robocopy exit code $rcExit"
            $copyFailed = $true
        } else {
            $fileCount = Parse-RobocopyCopiedCount -Output $script:RobocopyOutput
            Log "$fileCount files restored"
        }
    } finally {
        Unmount-AndCleanup -MountPath $MountPath -DiskNumber $diskNumber
    }

    if ($copyFailed) { return 1 }
    Log "Automatic restore of $SourcePath completed successfully"
    return 0
}

# When dot-sourced (e.g. by Pester), load functions but skip main logic
if ($MyInvocation.InvocationName -eq '.') { return }

# --- Entry point ---

# When invoked via SSH with command= restriction, validate and dispatch
$sshResult = Test-SshCommand
if ($sshResult -eq 'rejected') { exit 1 }
if ($null -ne $sshResult) {
    $env:SSH_ORIGINAL_COMMAND = $null
    cmd /c "`"C:\Program Files\filerestore\filerestore.bat`" $sshResult"
    exit $LASTEXITCODE
}

$rc = Invoke-FileRestore $args
exit $rc
