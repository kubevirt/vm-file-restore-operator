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

# When invoked via SSH with command= restriction, validate and extract arguments
if ($env:SSH_ORIGINAL_COMMAND) {
    # Verify command starts with allowed script path
    if ($env:SSH_ORIGINAL_COMMAND -notmatch '^"?C:\\Program Files\\filerestore\\filerestore\.bat"?') {
        Write-Host "ERROR: Only filerestore.bat commands are allowed" -ForegroundColor Red
        exit 1
    }

    # Strip the script path to get just the arguments
    $arguments = $env:SSH_ORIGINAL_COMMAND -replace '^"?C:\\Program Files\\filerestore\\filerestore\.bat"?\s*', ''
    $env:SSH_ORIGINAL_COMMAND = $null

    # Re-invoke the bat file with arguments using cmd.exe
    cmd /c "`"C:\Program Files\filerestore\filerestore.bat`" $arguments"
    exit $LASTEXITCODE
}

function Show-Usage {
    Write-Host "Usage:"
    Write-Host "  filerestore.bat restore --serial <SERIAL> --mount-path <PATH> [--source-path <PATH>]"
    Write-Host "  filerestore.bat cleanup --mount-path <PATH>"
    exit 1
}

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
        cmd /c rmdir "$MountPath" 2>$null
    }
    if (Test-Path $MountPath) {
        Write-Host "WARNING: Could not remove junction $MountPath"
    }

    # Set disk offline after removing the junction
    if ($DiskNumber -ge 0) {
        Set-Disk -Number $DiskNumber -IsOffline $true -ErrorAction SilentlyContinue
    }
}

# --- Parse arguments ---
if ($args.Count -lt 1) { Show-Usage }

$Mode = $args[0]
if ($Mode -notin @('restore', 'cleanup')) {
    Write-Host "ERROR: Unknown mode: $Mode"
    Show-Usage
}

$Serial = ''
$MountPath = ''
$SourcePath = ''
$i = 1
while ($i -lt $args.Count) {
    switch ($args[$i]) {
        '--serial'      { $Serial = $args[$i + 1]; $i += 2 }
        '--mount-path'  { $MountPath = $args[$i + 1]; $i += 2 }
        '--source-path' { $SourcePath = $args[$i + 1]; $i += 2 }
        default {
            Write-Host "ERROR: Unknown argument: $($args[$i])"
            Show-Usage
        }
    }
}

if (-not $MountPath) {
    Write-Host "ERROR: --mount-path is required"
    Show-Usage
}

# --- Cleanup mode: unmount and remove mount point ---
if ($Mode -eq 'cleanup') {
    Unmount-AndCleanup -MountPath $MountPath
    Write-Host "Cleanup of $MountPath completed"
    exit 0
}

# --- Restore mode requires --serial ---
if (-not $Serial) {
    Write-Host "ERROR: --serial is required for $Mode"
    Show-Usage
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
    exit 1
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
    exit 1
}

# Get or assign a drive letter (Windows does not always auto-assign one)
$driveLetter = $partition.DriveLetter
if (-not $driveLetter) {
    Add-PartitionAccessPath -DiskNumber $diskNumber -PartitionNumber $partition.PartitionNumber -AssignDriveLetter
    $partition = Get-Partition -DiskNumber $diskNumber -PartitionNumber $partition.PartitionNumber
    $driveLetter = $partition.DriveLetter
    if (-not $driveLetter) {
        Write-Host "ERROR: Could not assign a drive letter to partition on disk $diskNumber"
        exit 1
    }
    Write-Host "Assigned drive letter ${driveLetter}: to partition on disk $diskNumber"
}

# --- Unlock BitLocker volume (must use the real drive letter, not a junction) ---
$BitLockerPasswordFile = "C:\Program Files\filerestore\lockfile.txt"
if (Test-Path $BitLockerPasswordFile) {
    $recoveryPassword = (Get-Content -Path $BitLockerPasswordFile -Raw).Trim()
    Write-Host "Unlocking BitLocker volume at ${driveLetter}: ..."
    Unlock-BitLocker -MountPoint "${driveLetter}:" -RecoveryPassword $recoveryPassword
    Write-Host "BitLocker volume unlocked"
}

# Create a junction point to redirect $MountPath to the drive
if (Test-Path $MountPath) { cmd /c rmdir "$MountPath" 2>$null }
cmd /c mklink /J "$MountPath" "${driveLetter}:\" | Out-Null
if (-not (Test-Path $MountPath)) {
    Write-Host "ERROR: Failed to create junction from $MountPath to ${driveLetter}:\"
    exit 1
}
Set-Disk -Number $diskNumber -IsReadOnly $true
Write-Host "Disk mounted at $MountPath (junction to ${driveLetter}:\)"

# --- Manual mode: stop here, leave the volume mounted ---
if (-not $SourcePath) {
    Write-Host "Volume mounted at $MountPath for manual restore operations"
    exit 0
}

# --- Automatic mode: wrap in try/finally to guarantee cleanup ---
$copyFailed = $false
try {
    # Normalize: Strip trailing backslash
    $SourcePath = $SourcePath.TrimEnd('\')

    # Construct relative backup path
    # For full disk snapshots, strip drive letter and leading backslash
    # "C:\test" -> "test", "C:\foo\bar" -> "foo\bar"
    $RelativePath = $SourcePath -replace '^[A-Za-z]:\\', ''
    $BackupPath = Join-Path $MountPath $RelativePath

    # Validate source path on the backup volume (checked after mount)
    if (-not (Test-Path $BackupPath)) {
        Write-Host "ERROR: Source path $BackupPath does not exist on the backup volume"
        exit 1
    }

    # Copy files FROM the backup volume TO the original guest location
    if (Test-Path $BackupPath -PathType Leaf) {
        $srcDir = Split-Path $BackupPath -Parent
        $fileName = Split-Path $BackupPath -Leaf
        $destDir = Split-Path $SourcePath -Parent
        New-Item -ItemType Directory -Path $destDir -Force | Out-Null
        robocopy "$srcDir" "$destDir" "$fileName" /COPY:DATS /R:1 /W:1
    } else {
        New-Item -ItemType Directory -Path $SourcePath -Force | Out-Null
        robocopy "$BackupPath" "$SourcePath" /E /COPY:DATS /R:1 /W:1
    }

    # robocopy exit codes: 0-7 are success (bits indicate copied/extra/mismatched files), 8+ are errors
    if ($LASTEXITCODE -ge 8) {
        Write-Host "ERROR: File copy failed with robocopy exit code $LASTEXITCODE"
        $copyFailed = $true
    }
} finally {
    Unmount-AndCleanup -MountPath $MountPath -DiskNumber $diskNumber
}

if ($copyFailed) { exit 1 }
Write-Host "Automatic restore of $SourcePath completed successfully"
exit 0
