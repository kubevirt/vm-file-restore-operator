#!/bin/bash
# setup.sh - Configure a Linux VM for the VM File Restore Operator
#
# Usage:
#   sudo ./setup.sh "ssh-ed25519 AAAA...xyz"
#
# This script:
# - Creates the 'filerestore' user with sudo access
# - Configures SSH key authentication
# - Sets up passwordless sudo for the restore script
# - Downloads and installs the filerestore.sh helper script

set -e

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "ERROR: This script must be run as root or with sudo"
    echo "Usage: sudo $0 \"ssh-ed25519 AAAA...xyz\""
    exit 1
fi

# Check argument
if [ $# -ne 1 ]; then
    echo "ERROR: Public key argument required"
    echo "Usage: sudo $0 \"ssh-ed25519 AAAA...xyz\""
    exit 1
fi

PUB_KEY="$1"

# Validate public key format (basic check)
if [[ ! "$PUB_KEY" =~ ^ssh- ]]; then
    echo "ERROR: Public key must start with 'ssh-' (e.g., ssh-ed25519, ssh-rsa)"
    exit 1
fi

echo "Setting up VM for file restore operator..."

# Detect sudo group (wheel for RHEL/Fedora, sudo for Debian/Ubuntu)
if getent group wheel >/dev/null 2>&1; then
    SUDO_GROUP="wheel"
elif getent group sudo >/dev/null 2>&1; then
    SUDO_GROUP="sudo"
else
    echo "ERROR: Neither 'wheel' nor 'sudo' group found on this system."
    echo "The filerestore user requires sudo access to mount volumes."
    echo "Please install sudo and configure sudoers, then retry."
    exit 1
fi

# Create filerestore user
echo "Creating filerestore user..."
if id -u filerestore >/dev/null 2>&1; then
    echo "  User 'filerestore' already exists, skipping creation"
else
    useradd -m -s /bin/bash -G "$SUDO_GROUP" filerestore
    echo "  Created user with group: $SUDO_GROUP"
fi

# Set up SSH directory
echo "Setting up SSH directory..."
mkdir -p ~filerestore/.ssh
chmod 700 ~filerestore/.ssh

# Add public key with command restriction
echo "Installing SSH public key..."
linuxHelperScript="/usr/local/bin/filerestore.sh"
RESTRICTED_KEY="command=\"$linuxHelperScript\" $PUB_KEY"
if [ -f ~filerestore/.ssh/authorized_keys ] && grep -qF "$PUB_KEY" ~filerestore/.ssh/authorized_keys; then
    echo "  Key already exists, skipping"
else
    if [ -f ~filerestore/.ssh/authorized_keys ]; then
        echo "$RESTRICTED_KEY" >> ~filerestore/.ssh/authorized_keys
        echo "  Key added to existing authorized_keys (command-restricted)"
    else
        echo "$RESTRICTED_KEY" > ~filerestore/.ssh/authorized_keys
        echo "  Key installed in new authorized_keys (command-restricted)"
    fi
fi
chmod 600 ~filerestore/.ssh/authorized_keys
chown -R filerestore:filerestore ~filerestore/.ssh
echo "  Key: ${PUB_KEY:0:30}..."

# Configure sudoers
echo "Configuring passwordless sudo..."
echo "filerestore ALL=(ALL) NOPASSWD: /usr/local/bin/filerestore.sh" > /etc/sudoers.d/filerestore
chmod 440 /etc/sudoers.d/filerestore

# Validate sudoers file
if ! visudo -c -f /etc/sudoers.d/filerestore >/dev/null 2>&1; then
    echo "ERROR: Invalid sudoers configuration"
    rm -f /etc/sudoers.d/filerestore
    exit 1
fi
echo "  Sudoers configured: /etc/sudoers.d/filerestore"

# Install helper script (prefer a pre-staged file for offline / QE installs)
echo "Installing filerestore.sh helper script..."
STAGED_HELPER="/tmp/filerestore-operator-helper.sh"
SCRIPT_URL="https://raw.githubusercontent.com/kubevirt/vm-file-restore-operator/refs/heads/main/guest-helpers/linux/filerestore.sh"

if [ -f "$STAGED_HELPER" ]; then
    cp "$STAGED_HELPER" /usr/local/bin/filerestore.sh
    echo "  Installed from staged file: $STAGED_HELPER"
elif command -v curl >/dev/null 2>&1; then
    curl -sSL -o /usr/local/bin/filerestore.sh "$SCRIPT_URL"
    echo "  Downloaded from: $SCRIPT_URL"
elif command -v wget >/dev/null 2>&1; then
    wget -q -O /usr/local/bin/filerestore.sh "$SCRIPT_URL"
    echo "  Downloaded from: $SCRIPT_URL"
else
    echo "ERROR: No staged helper at $STAGED_HELPER, and neither curl nor wget found."
    exit 1
fi

chmod +x /usr/local/bin/filerestore.sh
echo "  Installed: /usr/local/bin/filerestore.sh"

# Verify installation
echo ""
echo "Setup complete! Verifying..."
echo "  User: $(id filerestore)"
echo "  SSH key: $(wc -l < ~filerestore/.ssh/authorized_keys) key(s) installed"
echo "  Sudoers: $(grep filerestore /etc/sudoers.d/filerestore)"
if [ -x /usr/local/bin/filerestore.sh ]; then
    echo "  Helper script: /usr/local/bin/filerestore.sh (executable)"
else
    echo "  Helper script: ERROR - not found or not executable"
fi
echo ""
echo "VM is ready for file restore operations!"
