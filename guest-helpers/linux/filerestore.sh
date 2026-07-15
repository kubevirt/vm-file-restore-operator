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
    # Split on whitespace (no eval — avoids shell injection).
    # Note: arguments containing spaces or quotes are not supported via SSH.
    read -ra _args <<< "${SSH_ORIGINAL_COMMAND#/usr/local/bin/filerestore.sh}" || true
    set -- "${_args[@]}"
    unset SSH_ORIGINAL_COMMAND  # Clear to prevent loops
fi

# Re-execute with sudo if not running as root (mount/umount require root)
# FILERESTORE_SKIP_ROOT_CHECK allows running under BATS without real root ($EUID is readonly)
if [ "${FILERESTORE_SKIP_ROOT_CHECK:-}" != "1" ] && [ "$EUID" -ne 0 ]; then
    exec sudo "$0" "$@"
fi

usage() {
    echo "Usage:"
    echo "  $0 restore --serial <SERIAL> --mount-path <PATH> [--source-path <PATH>]"
    echo "  $0 cleanup --mount-path <PATH>"
    exit 1
}

# try_umount attempts to unmount the given path, falling back to lazy unmount.
try_umount() {
    local target="$1"
    local err
    if ! err=$(umount "$target" 2>&1); then
        log "WARNING: Regular unmount of $target failed ($err), attempting lazy unmount"
        err=$(umount -l "$target" 2>&1) || log "WARNING: Lazy unmount of $target also failed: $err"
    fi
}

# get_mount_opts returns filesystem-specific read-only mount options.
# Snapshots often have dirty journals; these options skip replay that would
# require write access and fail on a read-only mount.
get_mount_opts() {
    local fstype="$1"
    case "$fstype" in
        ext3|ext4) echo "ro,noload" ;;      # noload: skip journal replay
        xfs) echo "ro,norecovery,nouuid" ;; # norecovery: skip log replay; nouuid: allow duplicate UUID mount
        *) echo "ro" ;;
    esac
}

# unmount_and_cleanup unmounts the given path and removes the mount point directory.
# If an LVM state file exists, deactivates and removes the cloned VG first.
# Uses lazy unmount as fallback if regular unmount fails (e.g., device busy).
# Retries rm in case the kernel hasn't fully released the mount point yet.
unmount_and_cleanup() {
    local mnt="$1"
    local _cleanup_ok=0
    local sync_rc=0
    timeout 10 sync || sync_rc=$?
    if [ "$sync_rc" -eq 124 ]; then
        log "WARNING: sync timed out after 10s, proceeding with unmount"
    elif [ "$sync_rc" -ne 0 ]; then
        log "WARNING: sync failed (exit $sync_rc), proceeding with unmount"
    fi

    if [ -f "${mnt}.lvm_vg" ]; then
        local vg_name
        vg_name=$(cat "${mnt}.lvm_vg")
        if [ -z "$vg_name" ]; then
            log "WARNING: LVM state file ${mnt}.lvm_vg exists but is empty, skipping LVM cleanup"
            try_umount "$mnt"
        else
            log "LVM cleanup: deactivating VG $vg_name"

            # Unmount all mount points under mnt (handles multi-LV case; reverse order)
            local submounts_raw
            # Escape regex metacharacters in the mount path so grep -E matches it literally
            local escaped_mnt
            # shellcheck disable=SC2016
            escaped_mnt=$(printf '%s' "$mnt" | sed 's/[.[\*^$()+?{|\\]/\\&/g')
            if ! submounts_raw=$(findmnt -rn -o TARGET 2>&1); then
                log "WARNING: findmnt failed ($submounts_raw), falling back to umount of $mnt"
                try_umount "$mnt"
            else
                local submounts
                submounts=$(echo "$submounts_raw" | grep -E "^${escaped_mnt}(/|$)" | sort -r || true)
                while IFS= read -r submnt; do
                    [ -z "$submnt" ] && continue
                    try_umount "$submnt"
                done <<< "$submounts"
            fi

            local vg_err
            if vg_err=$(vgchange --devicesfile "" -an "$vg_name" 2>&1); then
                vg_err=$(vgremove --devicesfile "" -f "$vg_name" 2>&1) || {
                    log "WARNING: Failed to remove VG $vg_name: $vg_err"
                    _cleanup_ok=1
                }
            else
                log "WARNING: Failed to deactivate VG $vg_name: $vg_err — skipping removal to avoid corruption"
                _cleanup_ok=1
            fi
        fi
        rm -f "${mnt}.lvm_vg"
    else
        try_umount "$mnt"
    fi

    for _ in 1 2 3; do
        rm -rf "$mnt" 2>/dev/null && return "$_cleanup_ok"
        sleep 1
    done
    log "WARNING: Could not remove $mnt"
    return 1
}

# Allow sourcing for unit tests without executing main logic
if [ "${FILERESTORE_SOURCED:-}" = "1" ]; then
    # shellcheck disable=SC2317
    return 0 2>/dev/null || true
fi

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
            [ -n "${2:-}" ] || { log_err "$1 requires a value"; usage; }
            SERIAL="$2"
            shift 2
            ;;
        --mount-path)
            [ -n "${2:-}" ] || { log_err "$1 requires a value"; usage; }
            MOUNT_PATH="$2"
            shift 2
            ;;
        --source-path)
            [ -n "${2:-}" ] || { log_err "$1 requires a value"; usage; }
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
    if ! unmount_and_cleanup "$MOUNT_PATH"; then
        log "WARNING: Cleanup of $MOUNT_PATH completed with errors"
        exit 1
    fi
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

# Track the effective mount point for rsync (may differ from MOUNT_PATH for multi-LV)
EFFECTIVE_MOUNT="$MOUNT_PATH"

if [ "$FSTYPE" = "LVM2_member" ]; then
    # --- LVM flow: resolve UUID collision, activate LVs, mount ---
    if ! command -v vgimportclone >/dev/null 2>&1; then
        log_err "LVM2_member detected but lvm2 tools not installed (install lvm2 package)"
        exit 1
    fi

    LVM_CLONED_VG="filerestore_${SERIAL}"
    log "LVM detected on /dev/$DEVICE, cloning VG as $LVM_CLONED_VG"

    # Clean up stale VG from a previous failed restore
    if vgs --devicesfile "" "$LVM_CLONED_VG" &>/dev/null; then
        log "WARNING: Stale VG $LVM_CLONED_VG found from previous run, removing"
        stale_err=""
        if ! stale_err=$(vgchange --devicesfile "" -an "$LVM_CLONED_VG" 2>&1); then
            log_err "Failed to deactivate stale VG $LVM_CLONED_VG: $stale_err"
            exit 1
        fi
        if ! stale_err=$(vgremove --devicesfile "" -f "$LVM_CLONED_VG" 2>&1); then
            log_err "Failed to remove stale VG $LVM_CLONED_VG: $stale_err"
            exit 1
        fi
    fi

    # Use --devicesfile "" to bypass the LVM devices file — the snapshot has
    # duplicate PV/VG UUIDs so the device can't be registered normally.
    if ! vgimportclone --devicesfile "" -n "$LVM_CLONED_VG" "/dev/$DEVICE"; then
        log_err "vgimportclone failed for /dev/$DEVICE"
        exit 1
    fi

    # Write state file immediately so cleanup can find the cloned VG on any failure
    if ! echo "$LVM_CLONED_VG" > "${MOUNT_PATH}.lvm_vg"; then
        log_err "Failed to write LVM state file, cleaning up cloned VG"
        _emerg_err=$(vgchange --devicesfile "" -an "$LVM_CLONED_VG" 2>&1) || log_err "Emergency VG deactivation failed: $_emerg_err"
        _emerg_err=$(vgremove --devicesfile "" -f "$LVM_CLONED_VG" 2>&1) || log_err "Emergency VG removal failed: $_emerg_err"
        exit 1
    fi

    _vgscan_err=$(vgscan --devicesfile "" --cache 2>&1) || log "WARNING: vgscan --cache failed: $_vgscan_err (non-fatal)"
    if ! vgchange --devicesfile "" -ay "$LVM_CLONED_VG"; then
        log_err "Failed to activate cloned VG $LVM_CLONED_VG"
        unmount_and_cleanup "$MOUNT_PATH"
        exit 1
    fi
    _udevadm_err=$(udevadm settle --timeout=5 2>&1) || log "WARNING: udevadm settle failed: $_udevadm_err"

    # Discover and mount LVs
    if ! LV_LIST=$(lvs --devicesfile "" --noheadings -o lv_name "$LVM_CLONED_VG" 2>&1); then
        log_err "Failed to list LVs in VG $LVM_CLONED_VG: $LV_LIST"
        unmount_and_cleanup "$MOUNT_PATH"
        exit 1
    fi
    LV_LIST=$(echo "$LV_LIST" | tr -d ' ')
    MOUNTED_COUNT=0

    # Count LVs with a mountable filesystem to decide layout
    MOUNTABLE_COUNT=0
    for LV_NAME in $LV_LIST; do
        LV_FSTYPE=$(blkid -o value -s TYPE "/dev/$LVM_CLONED_VG/$LV_NAME" 2>/dev/null || true)
        [ -n "$LV_FSTYPE" ] && MOUNTABLE_COUNT=$((MOUNTABLE_COUNT + 1))
    done

    for LV_NAME in $LV_LIST; do
        LV_PATH="/dev/$LVM_CLONED_VG/$LV_NAME"
        LV_FSTYPE=$(blkid -o value -s TYPE "$LV_PATH" 2>/dev/null || true)

        if [ -z "$LV_FSTYPE" ]; then
            log "Skipping LV $LV_NAME (no filesystem detected)"
            continue
        fi

        if [ "$MOUNTABLE_COUNT" -eq 1 ]; then
            LV_MOUNT="$MOUNT_PATH"
        else
            LV_MOUNT="$MOUNT_PATH/$LV_NAME"
            mkdir -p "$LV_MOUNT"
        fi

        LV_MOUNT_OPTS=$(get_mount_opts "$LV_FSTYPE")
        log "Mounting $LV_PATH (fstype=$LV_FSTYPE) at $LV_MOUNT with options: $LV_MOUNT_OPTS"
        if ! mount -o "$LV_MOUNT_OPTS" "$LV_PATH" "$LV_MOUNT"; then
            log_err "Failed to mount $LV_PATH at $LV_MOUNT"
            continue
        fi
        MOUNTED_COUNT=$((MOUNTED_COUNT + 1))
    done

    if [ "$MOUNTED_COUNT" -gt 0 ] && [ "$MOUNTED_COUNT" -lt "$MOUNTABLE_COUNT" ]; then
        log "WARNING: Only $MOUNTED_COUNT of $MOUNTABLE_COUNT mountable LVs were mounted"
    fi
    if [ "$MOUNTED_COUNT" -eq 0 ]; then
        log_err "No LVs could be mounted from VG $LVM_CLONED_VG"
        unmount_and_cleanup "$MOUNT_PATH"
        exit 1
    fi
else
    # --- Non-LVM flow: direct filesystem mount ---
    MOUNT_OPTS=$(get_mount_opts "$FSTYPE")
    log "Mounting /dev/$DEVICE (fstype=$FSTYPE) with options: $MOUNT_OPTS"
    if ! mount -o "$MOUNT_OPTS" "/dev/$DEVICE" "$MOUNT_PATH"; then
        log_err "Failed to mount /dev/$DEVICE at $MOUNT_PATH"
        exit 1
    fi
fi

# --- Manual mode: stop here, leave the volume mounted ---
if [ -z "$SOURCE_PATH" ]; then
    log "Volume mounted at $MOUNT_PATH for manual restore operations"
    exit 0
fi

# --- For multi-LV, find which sub-mount contains the source path ---
if [ "$FSTYPE" = "LVM2_member" ] && [ "$MOUNTABLE_COUNT" -gt 1 ]; then
    FOUND_MOUNT=""
    for LV_NAME in $LV_LIST; do
        if [ -e "$MOUNT_PATH/$LV_NAME/.$SOURCE_PATH" ]; then
            FOUND_MOUNT="$MOUNT_PATH/$LV_NAME"
            break
        fi
    done
    if [ -z "$FOUND_MOUNT" ]; then
        log_err "Source path $SOURCE_PATH not found in any LV of VG $LVM_CLONED_VG"
        unmount_and_cleanup "$MOUNT_PATH"
        exit 1
    fi
    EFFECTIVE_MOUNT="$FOUND_MOUNT"
fi

# --- Validate source path on the source volume (checked after mount) ---
if [ ! -e "$EFFECTIVE_MOUNT/.$SOURCE_PATH" ]; then
    log_err "Source path $EFFECTIVE_MOUNT/.$SOURCE_PATH does not exist on the source volume"
    unmount_and_cleanup "$MOUNT_PATH"
    exit 1
fi

# --- Automatic mode: copy files FROM the source volume back to the guest root ---
if ! rsync -avR "$EFFECTIVE_MOUNT/.$SOURCE_PATH" /; then
    log_err "Failed to restore $SOURCE_PATH from source volume"
    unmount_and_cleanup "$MOUNT_PATH"
    exit 1
fi

if ! unmount_and_cleanup "$MOUNT_PATH"; then
    log "WARNING: Cleanup had errors, but file restore itself succeeded"
fi
log "Automatic restore of $SOURCE_PATH completed successfully"
