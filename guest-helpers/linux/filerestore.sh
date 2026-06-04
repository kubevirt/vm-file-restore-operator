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

set -e

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
    umount "$mnt" 2>/dev/null || umount -l "$mnt" 2>/dev/null || true
    for i in 1 2 3; do
        rm -rf "$mnt" 2>/dev/null && return 0
        sleep 1
    done
    echo "WARNING: Could not remove $mnt"
}

if [ $# -lt 1 ]; then
    usage
fi

MODE="$1"; shift

case "$MODE" in
    restore|cleanup) ;;
    *) echo "ERROR: Unknown mode: $MODE"; usage ;;
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
            echo "ERROR: Unknown argument: $1"
            usage
            ;;
    esac
done

if [ -z "$MOUNT_PATH" ]; then
    echo "ERROR: --mount-path is required"
    usage
fi

# --- Cleanup mode: sync, unmount, remove mount point ---
if [ "$MODE" = "cleanup" ]; then
    unmount_and_cleanup "$MOUNT_PATH"
    echo "Cleanup of $MOUNT_PATH completed"
    exit 0
fi

# --- Restore mode requires --serial ---
if [ -z "$SERIAL" ]; then
    echo "ERROR: --serial is required for $MODE"
    usage
fi

# --- Find the device by serial number ---
DEVICE=$(lsblk -o NAME,SERIAL -n | grep "$SERIAL" | awk '{print $1}')
if [ -z "$DEVICE" ]; then
    echo "ERROR: Device with serial $SERIAL not found"
    exit 1
fi
echo "Found device: /dev/$DEVICE"

# If the device is a whole disk (no filesystem), find the largest partition
# that has a filesystem. Snapshots of VM disks typically contain a partition table.
FSTYPE=$(blkid -o value -s TYPE "/dev/$DEVICE" 2>/dev/null || true)
if [ -z "$FSTYPE" ]; then
    PART=$(lsblk -n -o NAME,FSTYPE -l "/dev/$DEVICE" | awk '$2 != "" {print $1}' | tail -1)
    if [ -n "$PART" ]; then
        echo "Device is partitioned, using partition: /dev/$PART"
        DEVICE="$PART"
        FSTYPE=$(blkid -o value -s TYPE "/dev/$DEVICE" 2>/dev/null || true)
    else
        echo "ERROR: No mountable filesystem found on /dev/$DEVICE or its partitions"
        exit 1
    fi
fi

# --- Mount ---
mkdir -p "$MOUNT_PATH"

# noload skips ext journal replay (which requires write access), but is only
# valid for ext3/ext4. Other filesystems (e.g. ntfs/fuseblk, xfs) just need ro.
MOUNT_OPTS="ro"
case "$FSTYPE" in
    ext3|ext4) MOUNT_OPTS="ro,noload" ;;
esac
echo "Mounting /dev/$DEVICE (fstype=$FSTYPE) with options: $MOUNT_OPTS"
mount -o "$MOUNT_OPTS" "/dev/$DEVICE" "$MOUNT_PATH"

# --- Manual mode: stop here, leave the volume mounted ---
if [ -z "$SOURCE_PATH" ]; then
    echo "Volume mounted at $MOUNT_PATH for manual restore operations"
    exit 0
fi

# --- Validate source path on the source volume (checked after mount) ---
if [ ! -e "$MOUNT_PATH/.$SOURCE_PATH" ]; then
    echo "ERROR: Source path $MOUNT_PATH/.$SOURCE_PATH does not exist on the source volume"
    unmount_and_cleanup "$MOUNT_PATH"
    exit 1
fi

# --- Automatic mode: copy files FROM the source volume back to the guest root ---
rsync -avR "$MOUNT_PATH/.$SOURCE_PATH" /

unmount_and_cleanup "$MOUNT_PATH"
echo "Automatic restore of $SOURCE_PATH completed successfully"
