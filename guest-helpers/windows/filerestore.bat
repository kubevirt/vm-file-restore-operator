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

function Show-Usage {
    Write-Host "Usage:"
    Write-Host "  filerestore.bat restore --serial <SERIAL> --mount-path <PATH> [--source-path <PATH>]"
    Write-Host "  filerestore.bat cleanup --mount-path <PATH>"
}

# Wrapper functions for native executables (enables Pester mocking)
function New-Junction { param([string]$Path, [string]$Target) cmd /c mklink /J "$Path" "$Target" | Out-Null }
function Remove-Junction { param([string]$Path) cmd /c rmdir "$Path" 2>$null }
function Invoke-Robocopy { param([string[]]$Arguments) & robocopy @Arguments; return $LASTEXITCODE }
function Read-FileContent { param([string]$Path) return (Get-Content -Path $Path -Raw).Trim() }

# Unmount-AndCleanup resolves the junction target to find the disk, sets the
# disk offline, and removes the junction point.
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
        Write-Host "WARNING: Could not remove junction $MountPath"
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
        Write-Host "ERROR: Only filerestore.bat commands are allowed" -ForegroundColor Red
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
        Write-Host "ERROR: Unknown mode: $Mode"
        Show-Usage; return 1
    }

    $Serial = ''
    $MountPath = ''
    $SourcePath = ''
    $i = 1
    while ($i -lt $Arguments.Count) {
        switch ($Arguments[$i]) {
            '--serial'      { $Serial = $Arguments[$i + 1]; $i += 2 }
            '--mount-path'  { $MountPath = $Arguments[$i + 1]; $i += 2 }
            '--source-path' { $SourcePath = $Arguments[$i + 1]; $i += 2 }
            default {
                Write-Host "ERROR: Unknown argument: $($Arguments[$i])"
                Show-Usage; return 1
            }
        }
    }

    if (-not $MountPath) {
        Write-Host "ERROR: --mount-path is required"
        Show-Usage; return 1
    }

    # --- Cleanup mode: unmount and remove mount point ---
    if ($Mode -eq 'cleanup') {
        Unmount-AndCleanup -MountPath $MountPath
        Write-Host "Cleanup of $MountPath completed"
        return 0
    }

    # --- Restore mode requires --serial ---
    if (-not $Serial) {
        Write-Host "ERROR: --serial is required for $Mode"
        Show-Usage; return 1
    }

    # --- Clear readonly and bring offline disks online ---

    $offLineDisks = Get-Disk | Where-Object IsOffline -eq $True

    foreach ($disk in $offLineDisks) {
        Write-Host "Found Offline Disk: $($disk.Number). Clearing readonly and bringing online..."

        # Clear the ReadOnly attribute at the disk level
        Set-Disk -Number $disk.Number -IsReadOnly $false

        # Bring the disk online
        Set-Disk -Number $disk.Number -IsOffline $false
    }

    # --- Find disk by serial number ---
    $disk = Get-Disk | Where-Object { $_.SerialNumber -and $_.SerialNumber.Trim() -eq $Serial }
    if (-not $disk) {
        Write-Host "ERROR: Disk with serial $Serial not found"
        return 1
    }
    $diskNumber = $disk.Number
    Write-Host "Found disk: Disk $diskNumber (Serial: $Serial)"

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
        Write-Host "ERROR: No usable partition found on disk $diskNumber"
        return 1
    }

    # Get or assign a drive letter (Windows does not always auto-assign one)
    $driveLetter = $partition.DriveLetter
    if (-not $driveLetter) {
        Add-PartitionAccessPath -DiskNumber $diskNumber -PartitionNumber $partition.PartitionNumber -AssignDriveLetter
        $partition = Get-Partition -DiskNumber $diskNumber -PartitionNumber $partition.PartitionNumber
        $driveLetter = $partition.DriveLetter
        if (-not $driveLetter) {
            Write-Host "ERROR: Could not assign a drive letter to partition on disk $diskNumber"
            return 1
        }
        Write-Host "Assigned drive letter ${driveLetter}: to partition on disk $diskNumber"
    }

    # --- Unlock BitLocker volume (must use the real drive letter, not a junction) ---
    $BitLockerPasswordFile = "C:\Program Files\filerestore\lockfile.txt"
    if (Test-Path $BitLockerPasswordFile) {
        $recoveryPassword = Read-FileContent -Path $BitLockerPasswordFile
        Write-Host "Unlocking BitLocker volume at ${driveLetter}: ..."
        Unlock-BitLocker -MountPoint "${driveLetter}:" -RecoveryPassword $recoveryPassword
        Write-Host "BitLocker volume unlocked"
    }

    # Create a junction point to redirect $MountPath to the drive
    if (Test-Path $MountPath) { Remove-Junction -Path $MountPath }
    New-Junction -Path $MountPath -Target "${driveLetter}:\"
    if (-not (Test-Path $MountPath)) {
        Write-Host "ERROR: Failed to create junction from $MountPath to ${driveLetter}:\"
        return 1
    }
    Set-Disk -Number $diskNumber -IsReadOnly $true
    Write-Host "Disk mounted at $MountPath (junction to ${driveLetter}:\)"

    # --- Manual mode: stop here, leave the volume mounted ---
    if (-not $SourcePath) {
        Write-Host "Volume mounted at $MountPath for manual restore operations"
        return 0
    }

    # --- Automatic mode: wrap in try/finally to guarantee cleanup ---
    $copyFailed = $false
    try {
        # Construct relative backup path
        # For full disk snapshots, strip drive letter and leading backslash
        # "C:\test" -> "test", "C:\foo\bar" -> "foo\bar"
        $RelativePath = $SourcePath -replace '^[A-Za-z]:\\', ''
        $BackupPath = Join-Path $MountPath $RelativePath

        # Validate source path on the backup volume (checked after mount)
        if (-not (Test-Path $BackupPath)) {
            Write-Host "ERROR: Source path $BackupPath does not exist on the backup volume"
            return 1
        }

        # Copy files FROM the backup volume TO the original guest location
        if (Test-Path $BackupPath -PathType Leaf) {
            $srcDir = Split-Path $BackupPath -Parent
            $fileName = Split-Path $BackupPath -Leaf
            $destDir = Split-Path $SourcePath -Parent
            New-Item -ItemType Directory -Path $destDir -Force | Out-Null
            $rcExit = Invoke-Robocopy -Arguments @("$srcDir", "$destDir", "$fileName", '/COPY:DATS', '/R:1', '/W:1')
        } else {
            New-Item -ItemType Directory -Path $SourcePath -Force | Out-Null
            $rcExit = Invoke-Robocopy -Arguments @("$BackupPath", "$SourcePath", '/E', '/COPY:DATS', '/R:1', '/W:1')
        }

        # robocopy exit codes: 0-7 are success (bits indicate copied/extra/mismatched files), 8+ are errors
        if ($rcExit -ge 8) {
            Write-Host "ERROR: File copy failed with robocopy exit code $rcExit"
            $copyFailed = $true
        }
    } finally {
        Unmount-AndCleanup -MountPath $MountPath -DiskNumber $diskNumber
    }

    if ($copyFailed) { return 1 }
    Write-Host "Automatic restore of $SourcePath completed successfully"
    return 0
}

# Allow importing for unit tests without executing main logic
if ($env:FILERESTORE_TEST_MODE -eq '1') { return }

# --- Entry point ---

# When invoked via SSH with command= restriction, validate and re-invoke
$sshResult = Test-SshCommand
if ($sshResult -eq 'rejected') { exit 1 }
if ($null -ne $sshResult) {
    $env:SSH_ORIGINAL_COMMAND = $null
    cmd /c "`"C:\Program Files\filerestore\filerestore.bat`" $sshResult"
    exit $LASTEXITCODE
}

$rc = Invoke-FileRestore $args
exit $rc
