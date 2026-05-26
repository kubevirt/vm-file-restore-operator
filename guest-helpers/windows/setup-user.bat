# 2>nul & @echo off & goto :BATCH
<#
:BATCH
copy /y "%~f0" "%TEMP%\setup-user.ps1" >nul
powershell -NoProfile -ExecutionPolicy Bypass -File "%TEMP%\setup-user.ps1" %*
set _RC=%ERRORLEVEL%
del /q "%TEMP%\setup-user.ps1" 2>nul
exit /b %_RC%
: #>

# setup-user.bat - Create a Windows user with SSH public-key authentication.
#
# Usage:
#   setup-user.bat <USERNAME>

$ErrorActionPreference = 'Stop'

if ($args.Count -lt 1) {
    Write-Host "Usage: setup-user.bat <USERNAME>"
    exit 1
}

$User = $args[0]

# Create user and add to Administrators
net user $User /add
net localgroup Administrators $User /add
Write-Host "Created user $User and added to Administrators"

# Write authorized keys for admins
$PubKey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQCxxBZZIUTEGbosUuaHohjUuokLJrfS3pdA9piqqr7483yhVq+6VfuUu5efuGxDYrSsEPawHdIy8btH1cVwcod3I1HbwcQdzJDYyUQvsjE/sRZ477dPd255+NHSb+e6VqqHnk3F/aWlSPOVa3fadwDvOewy3Wkeq6wa2AYlEaFecjJ+tn5ql1upbLEdMKaqKR2iQg0cddwEmmeUaNyNFH6+W9QGm8NBgu/Ijg2uadCmVPmwr6BXv1oXIAvI99FxsPuxnzaXFtONpGOtw3bb8VjmLiw7/W72MgB4UZxCpNWLU1oCFitRcHtTWJOGzorpML21iz6tFNGXbnHz2dRwdfKuSY4vUGzbgVx5A+BY7AkkHjUUWQFEAXSLgZyPWXfBWoy1wd/6qeqIh6PV3pRH8QhBuRcj/AgyTPzwgnTtDPuOVwJtA9QD1zxet2pIP4+YlX944887SH8JT4f5N8ewJyKaJZpkxHL75gCM39iTXWi2WQyVudRFHZSzaMDIye2SupcCzFPnRnGV6xt4wJVERNh7dOI38vsXyxYjaQsuFHIfc+E/UtN0uAwA8FJMcuHPylutbDBqoYRjHyMkPcc0zbcZQkUgG7cMUnuIkuXu5B3gbAXskFIvvam9TGssYn1qDodejWr6bZeTnR40B+hpjR6FITHvrI+MOwdYQoArVfFNtQ== agilboa@redhat.com"
$AuthKeys = "C:\ProgramData\ssh\administrators_authorized_keys"
Set-Content -Path $AuthKeys -Value $PubKey
icacls $AuthKeys /inheritance:r
icacls $AuthKeys /grant "Administrators:F"
icacls $AuthKeys /grant "SYSTEM:F"
Write-Host "Wrote SSH public key to $AuthKeys"

# Ensure key and password auth are enabled in sshd_config
$sshdConfig = "C:\ProgramData\ssh\sshd_config"
(Get-Content $sshdConfig) `
    -replace '#PasswordAuthentication no','PasswordAuthentication yes' `
    -replace '#PubkeyAuthentication yes','PubkeyAuthentication yes' |
    Set-Content $sshdConfig

Restart-Service sshd
Write-Host "sshd restarted - user $User is ready for SSH access"
