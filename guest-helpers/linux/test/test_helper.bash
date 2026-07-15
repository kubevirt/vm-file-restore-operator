#!/bin/bash
# Shared setup/teardown for filerestore.sh BATS tests.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$SCRIPT_DIR/filerestore.sh"

bats_load_library bats-support
bats_load_library bats-assert

setup() {
    export FILERESTORE_SKIP_ROOT_CHECK=1
    export PATH="$BATS_TEST_DIRNAME/mocks:$PATH"
    export MOCK_CALL_LOG="$BATS_TEST_TMPDIR/mock_calls.log"
    > "$MOCK_CALL_LOG"

    # Default mock behaviors (tests override as needed)
    export MOCK_LSBLK_SERIAL_OUTPUT=""
    export MOCK_LSBLK_FSTYPE_OUTPUT=""
    export MOCK_LSBLK_EXIT=0
    export MOCK_BLKID_OUTPUT=""
    export MOCK_BLKID_EXIT=1
    export MOCK_MOUNT_EXIT=0
    export MOCK_UMOUNT_EXIT=0
    export MOCK_UMOUNT_LAZY_EXIT=0
    export MOCK_RSYNC_EXIT=0

    # LVM mock defaults
    export MOCK_VGS_EXIT=1  # default: VG not found (no stale VG)
    export MOCK_VGSCAN_EXIT=0
    export MOCK_VGIMPORTCLONE_EXIT=0
    export MOCK_VGCHANGE_AY_EXIT=0
    export MOCK_VGCHANGE_EXIT=0
    export MOCK_LVS_OUTPUT=""
    export MOCK_LVS_EXIT=0
    export MOCK_VGREMOVE_EXIT=0
    export MOCK_FINDMNT_OUTPUT=""
    export MOCK_FINDMNT_EXIT=0

    # Temp directory for fake mount points
    export TEST_MOUNT_DIR="$BATS_TEST_TMPDIR/mnt"
    mkdir -p "$TEST_MOUNT_DIR"
}

teardown() {
    [[ -n "${BATS_TEST_TMPDIR:-}" ]] && rm -rf "${BATS_TEST_TMPDIR:?}"/* 2>/dev/null || true
    unset SSH_ORIGINAL_COMMAND
}

# --- Test helper functions ---

# setup_device_mock configures a device found by serial "ABC123" with the given fstype.
setup_device_mock() {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="$1"
}

# setup_lvm_single_lv_mock configures mocks for a single LV (ext4) on LVM.
setup_lvm_single_lv_mock() {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  datalv"
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/datalv=ext4"
}

# create_test_source_file creates a file at the given source-path under TEST_MOUNT_DIR,
# using the ./path convention expected by rsync -avR.
# Usage: create_test_source_file "/data/file.txt" [base_dir]
create_test_source_file() {
    local source_path="$1"
    local base="${2:-$TEST_MOUNT_DIR}"
    mkdir -p "$base/.$(dirname "$source_path")"
    touch "$base/.$source_path"
}
