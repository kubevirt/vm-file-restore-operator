# 2>nul & @echo off & goto :BATCH
<#
:BATCH
copy /y "%~f0" "%TEMP%\setup.ps1" >nul
powershell -NoProfile -ExecutionPolicy Bypass -File "%TEMP%\setup.ps1" %*
set _RC=%ERRORLEVEL%
del /q "%TEMP%\setup.ps1" 2>nul
exit /b %_RC%
: #>

# setup.bat - Configure a Windows VM for the VM File Restore Operator
#
# Usage:
#   setup.bat "ssh-ed25519 AAAA...xyz"
#
# This script:
# - Creates the 'filerestore' user in Administrators group
# - Configures SSH key authentication
# - Downloads and installs the filerestore.bat helper script

$ErrorActionPreference = 'Stop'

# Check if running as Administrator
$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "ERROR: This script must be run as Administrator"
    Write-Host "Usage: setup.bat `"ssh-ed25519 AAAA...xyz`""
    exit 1
}

# Check argument
if ($args.Count -lt 1) {
    Write-Host "ERROR: Public key argument required"
    Write-Host "Usage: setup.bat `"ssh-ed25519 AAAA...xyz`""
    exit 1
}

$PubKey = $args[0]

# Validate public key format
if (-not $PubKey.StartsWith("ssh-")) {
    Write-Host "ERROR: Public key must start with 'ssh-' (e.g., ssh-ed25519, ssh-rsa)"
    exit 1
}

Write-Host "Setting up Windows VM for file restore operator..."

# Create filerestore user
Write-Host "Creating filerestore user..."
try {
    $existingUser = Get-LocalUser -Name "filerestore" -ErrorAction SilentlyContinue
    if ($existingUser) {
        Write-Host "  User 'filerestore' already exists, skipping creation"
    } else {
        # Generate random password (required by Windows but never used - SSH uses keys)
        Add-Type -AssemblyName System.Web
        $password = [System.Web.Security.Membership]::GeneratePassword(20, 3)
        $securePassword = ConvertTo-SecureString $password -AsPlainText -Force

        New-LocalUser -Name "filerestore" -Password $securePassword -PasswordNeverExpires -AccountNeverExpires -UserMayNotChangePassword -Description "VM File Restore Operator service account"
        Add-LocalGroupMember -Group "Administrators" -Member "filerestore"
        Write-Host "  Created user and added to Administrators group"
        Write-Host "  Password authentication disabled (SSH key-based only)"
    }
} catch {
    Write-Host "ERROR: Failed to create user: $_"
    exit 1
}

# Set up SSH authorized_keys for administrators
Write-Host "Installing SSH public key..."
$AuthKeysPath = "C:\ProgramData\ssh\administrators_authorized_keys"

# Ensure directory exists
$sshDir = Split-Path $AuthKeysPath
if (-not (Test-Path $sshDir)) {
    New-Item -ItemType Directory -Path $sshDir -Force | Out-Null
}

# Check if key already exists
$keyExists = $false
if (Test-Path $AuthKeysPath) {
    $existingKeys = Get-Content -Path $AuthKeysPath -ErrorAction SilentlyContinue
    if ($existingKeys -contains $PubKey) {
        $keyExists = $true
        Write-Host "  Key already exists, skipping"
    }
}

# Add public key if it doesn't exist
if (-not $keyExists) {
    if (Test-Path $AuthKeysPath) {
        # Append to existing file
        Add-Content -Path $AuthKeysPath -Value $PubKey -Encoding ASCII
        Write-Host "  Key added to existing authorized_keys"
    } else {
        # Create new file
        Set-Content -Path $AuthKeysPath -Value $PubKey -Encoding ASCII
        Write-Host "  Key installed in new authorized_keys"
    }

    # Set correct permissions (only SYSTEM and Administrators)
    icacls $AuthKeysPath /inheritance:r | Out-Null
    icacls $AuthKeysPath /grant "SYSTEM:F" | Out-Null
    icacls $AuthKeysPath /grant "Administrators:F" | Out-Null
}

Write-Host "  Key: $($PubKey.Substring(0, [Math]::Min(30, $PubKey.Length)))..."

# Configure SSH server
Write-Host "Configuring SSH server..."
$sshdConfigPath = "C:\ProgramData\ssh\sshd_config"

if (Test-Path $sshdConfigPath) {
    # Enable PubkeyAuthentication and disable PasswordAuthentication
    $config = Get-Content $sshdConfigPath
    $modified = $false

    if ($config -notmatch "^PubkeyAuthentication\s+yes") {
        $config = $config -replace '#\s*PubkeyAuthentication.*','PubkeyAuthentication yes'
        $modified = $true
    }

    if ($config -notmatch "^PasswordAuthentication\s+no") {
        $config = $config -replace '#\s*PasswordAuthentication.*','PasswordAuthentication no'
        if ($config -notmatch "^PasswordAuthentication\s+no") {
            # Line didn't exist, add it
            $config += "`nPasswordAuthentication no"
        }
        $modified = $true
    }

    if ($modified) {
        Set-Content -Path $sshdConfigPath -Value $config
        Write-Host "  Configured SSH for public key authentication only"
    }
} else {
    Write-Host "  WARNING: sshd_config not found at $sshdConfigPath"
}

# Ensure SSH service is running
Write-Host "Starting SSH service..."
try {
    $sshd = Get-Service sshd -ErrorAction SilentlyContinue
    if ($sshd) {
        if ($sshd.Status -ne 'Running') {
            Start-Service sshd
        }
        Set-Service -Name sshd -StartupType Automatic
        Write-Host "  SSH service is running and set to automatic startup"
    } else {
        Write-Host "  WARNING: sshd service not found. Please install OpenSSH Server."
    }
} catch {
    Write-Host "  WARNING: Could not configure SSH service: $_"
}

# Download and install helper script
Write-Host "Installing filerestore.bat helper script..."
$scriptUrl = "https://raw.githubusercontent.com/arnongilboa/vm-file-restore-operator/refs/heads/add-initial-operator/guest-helpers/windows/filerestore.bat"
$scriptPath = "C:\Program Files\filerestore\filerestore.bat"

# Create directory
$scriptDir = Split-Path $scriptPath
if (-not (Test-Path $scriptDir)) {
    New-Item -ItemType Directory -Path $scriptDir -Force | Out-Null
}

# Download script
try {
    Invoke-WebRequest -Uri $scriptUrl -OutFile $scriptPath -UseBasicParsing
    Write-Host "  Installed: $scriptPath"
} catch {
    Write-Host "ERROR: Failed to download helper script: $_"
    Write-Host "Please manually download from: $scriptUrl"
    exit 1
}

# Verify installation
Write-Host ""
Write-Host "Setup complete! Verifying..."
Write-Host "  User: filerestore (member of Administrators)"
Write-Host "  SSH key: $(if (Test-Path $AuthKeysPath) { 'installed' } else { 'ERROR - not found' })"
Write-Host "  SSH service: $((Get-Service sshd -ErrorAction SilentlyContinue).Status)"
Write-Host "  Helper script: $(if (Test-Path $scriptPath) { 'installed' } else { 'ERROR - not found' })"
Write-Host ""
Write-Host "VM is ready for file restore operations!"
