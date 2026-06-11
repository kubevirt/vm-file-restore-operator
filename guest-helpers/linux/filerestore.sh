#!/bin/bash
# filerestore.sh - Restore or cleanup a hotplugged volume identified by serial.
#
# Usage:
#   Restore (automatic):
#     filerestore.sh restore --serial <SERIAL> --mount-path <PATH> --source-path <PATH>
#
#   Restore (manual - mount read-only only):
#     filerestore.sh restore --serial <SERIAL> --mount-path <PATH>
#
#   Cleanup (unmount and remove mount point):
#     filerestore.sh cleanup --mount-path <PATH>
#
# When --source-path is omitted, the script runs in manual mode (mount only).
# Cleanup is a standalone operation that syncs, unmounts, and removes the mount point.
#
set -eo pipefail

log() { echo "[filerestore] $*"; }
log_err() { echo "[filerestore] ERROR: $*" >&2; }

# When invoked via SSH with command= restriction, validate and extract arguments
if [ -n "$SSH_ORIGINAL_COMMAND" ]; then
    # Verify command starts with allowed script path
    if [[ ! "$SSH_ORIGINAL_COMMAND" =~ ^/usr/local/bin/filerestore\.sh($|[[:space:]]) ]]; then
        log_err "Only filerestore.sh commands are allowed"
        exit 1
    fi
    # Extract arguments from SSH_ORIGINAL_COMMAND
    # Split remaining arguments on whitespace (no eval — avoids shell injection)
    read -ra _args <<< "${SSH_ORIGINAL_COMMAND#/usr/local/bin/filerestore.sh}" || true
    set -- "${_args[@]}"
    unset SSH_ORIGINAL_COMMAND  # Clear to prevent loops
fi

# Re-execute with sudo if not running as root (mount/umount require root)
if [ "$EUID" -ne 0 ]; then
    exec sudo "$0" "$@"
fi

usage() {
    echo "Usage:"
    echo "  $0 restore --serial <SERIAL> --mount-path <PATH> [--source-path <PATH>]"
    echo "  $0 cleanup --mount-path <PATH>"
    exit 1
}

# unmount_and_cleanup unmounts the given path and removes the mount point directory.
# Uses lazy unmount as fallback if regular unmount fails (e.g., device busy).
# Retries rm in case the kernel hasn't fully released the mount point yet.
unmount_and_cleanup() {
    local mnt="$1"
    sync
    if ! umount "$mnt" 2>/dev/null; then
        log "WARNING: Regular unmount of $mnt failed, attempting lazy unmount"
        umount -l "$mnt" 2>/dev/null || log "WARNING: Lazy unmount of $mnt also failed"
    fi
    for _ in 1 2 3; do
        rm -rf "$mnt" 2>/dev/null && return 0
        sleep 1
    done
    log "WARNING: Could not remove $mnt"
}

if [ $# -lt 1 ]; then
    usage
fi

MODE="$1"; shift

case "$MODE" in
    restore|cleanup) ;;
    *) log_err "Unknown mode: $MODE"; usage ;;
esac

SERIAL=""
MOUNT_PATH=""
SOURCE_PATH=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --serial)
            SERIAL="$2"
            shift 2
            ;;
        --mount-path)
            MOUNT_PATH="$2"
            shift 2
            ;;
        --source-path)
            SOURCE_PATH="$2"
            shift 2
            ;;
        *)
            log_err "Unknown argument: $1"
            usage
            ;;
    esac
done

if [ -z "$MOUNT_PATH" ]; then
    log_err "--mount-path is required"
    usage
fi

# --- Cleanup mode: sync, unmount, remove mount point ---
if [ "$MODE" = "cleanup" ]; then
    unmount_and_cleanup "$MOUNT_PATH"
    log "Cleanup of $MOUNT_PATH completed"
    exit 0
fi

# --- Restore mode requires --serial ---
if [ -z "$SERIAL" ]; then
    log_err "--serial is required for $MODE"
    usage
fi

# --- Find the device by serial number ---
DEVICE=$(lsblk -o NAME,SERIAL -n | awk -v serial="$SERIAL" '$2 == serial {print $1; exit}')
if [ -z "$DEVICE" ]; then
    log_err "Device with serial $SERIAL not found"
    exit 1
fi
log "Found device: /dev/$DEVICE"

# If the device is a whole disk (no filesystem), find the largest partition
# that has a filesystem. Snapshots of VM disks typically contain a partition table.
FSTYPE=$(blkid -o value -s TYPE "/dev/$DEVICE" 2>/dev/null || true)
if [ -z "$FSTYPE" ]; then
    PART=$(lsblk -n -o NAME,FSTYPE -l "/dev/$DEVICE" | awk '$2 != "" {print $1}' | tail -1)
    if [ -n "$PART" ]; then
        log "Device is partitioned, using partition: /dev/$PART"
        DEVICE="$PART"
        FSTYPE=$(blkid -o value -s TYPE "/dev/$DEVICE" 2>/dev/null || true)
        if [ -z "$FSTYPE" ]; then
            log_err "Could not determine filesystem type for partition /dev/$DEVICE"
            exit 1
        fi
    else
        log_err "No mountable filesystem found on /dev/$DEVICE or its partitions"
        exit 1
    fi
fi

# --- Mount ---
mkdir -p "$MOUNT_PATH"

# Filesystem-specific read-only mount options:
#   ext3/ext4: noload — skips journal replay, which requires write access and
#              would fail on a read-only mount of a snapshot with a dirty journal.
#   xfs:       norecovery — same purpose as noload (skips log replay). Without it,
#              XFS refuses to mount read-only if the log is dirty (exit code 32).
#              nouuid — allows mounting a snapshot whose UUID matches the already-
#              mounted original disk. XFS rejects duplicate UUIDs by default.
MOUNT_OPTS="ro"
case "$FSTYPE" in
    ext3|ext4) MOUNT_OPTS="ro,noload" ;;
    xfs) MOUNT_OPTS="ro,norecovery,nouuid" ;;
esac
log "Mounting /dev/$DEVICE (fstype=$FSTYPE) with options: $MOUNT_OPTS"
if ! mount -o "$MOUNT_OPTS" "/dev/$DEVICE" "$MOUNT_PATH"; then
    log_err "Failed to mount /dev/$DEVICE at $MOUNT_PATH"
    exit 1
fi

# --- Manual mode: stop here, leave the volume mounted ---
if [ -z "$SOURCE_PATH" ]; then
    log "Volume mounted at $MOUNT_PATH for manual restore operations"
    exit 0
fi

# --- Validate source path on the source volume (checked after mount) ---
if [ ! -e "$MOUNT_PATH/.$SOURCE_PATH" ]; then
    log_err "Source path $MOUNT_PATH/.$SOURCE_PATH does not exist on the source volume"
    unmount_and_cleanup "$MOUNT_PATH"
    exit 1
fi

# --- Automatic mode: copy files FROM the source volume back to the guest root ---
if ! rsync -avR "$MOUNT_PATH/.$SOURCE_PATH" /; then
    log_err "Failed to restore $SOURCE_PATH from source volume"
    unmount_and_cleanup "$MOUNT_PATH"
    exit 1
fi

unmount_and_cleanup "$MOUNT_PATH"
log "Automatic restore of $SOURCE_PATH completed successfully"
