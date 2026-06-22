#!/usr/bin/env bats
# Tests for guest-helpers/linux/filerestore.sh

load 'test_helper'

# =============================================================================
# SSH_ORIGINAL_COMMAND validation
# =============================================================================

@test "SSH: rejects disallowed command" {
    export SSH_ORIGINAL_COMMAND="/bin/rm -rf /"
    run "$SCRIPT"
    assert_failure
    assert_output --partial "Only filerestore.sh commands are allowed"
}

@test "SSH: accepts valid command and extracts args" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    export SSH_ORIGINAL_COMMAND="/usr/local/bin/filerestore.sh restore --serial ABC123 --mount-path $TEST_MOUNT_DIR"
    run "$SCRIPT"
    assert_success
    assert_output --partial "Volume mounted at $TEST_MOUNT_DIR for manual restore"
}

@test "SSH: valid command with no arguments shows usage" {
    export SSH_ORIGINAL_COMMAND="/usr/local/bin/filerestore.sh"
    run "$SCRIPT"
    assert_failure
    assert_output --partial "Usage:"
}

@test "SSH: rejects command with wrong script path" {
    export SSH_ORIGINAL_COMMAND="/usr/local/bin/evil.sh restore"
    run "$SCRIPT"
    assert_failure
    assert_output --partial "Only filerestore.sh commands are allowed"
}

@test "SSH: rejects path-suffix bypass attempt" {
    export SSH_ORIGINAL_COMMAND="/usr/local/bin/filerestore.sh-evil restore"
    run "$SCRIPT"
    assert_failure
    assert_output --partial "Only filerestore.sh commands are allowed"
}

# =============================================================================
# Argument parsing and validation
# =============================================================================

@test "args: no arguments shows usage" {
    run "$SCRIPT"
    assert_failure
    assert_output --partial "Usage:"
}

@test "args: unknown mode shows error" {
    run "$SCRIPT" bogus
    assert_failure
    assert_output --partial "Unknown mode: bogus"
}

@test "args: restore without --serial" {
    run "$SCRIPT" restore --mount-path /mnt
    assert_failure
    assert_output --partial "--serial is required"
}

@test "args: missing --mount-path" {
    run "$SCRIPT" restore --serial ABC
    assert_failure
    assert_output --partial "--mount-path is required"
}

@test "args: missing value --serial" {
    run "$SCRIPT" restore --serial
    assert_failure
    assert_output --partial "--serial requires a value"
}

@test "args: missing value --mount-path" {
    run "$SCRIPT" restore --serial ABC --mount-path
    assert_failure
    assert_output --partial "--mount-path requires a value"
}

@test "args: missing value --source-path" {
    run "$SCRIPT" restore --serial ABC --mount-path /mnt --source-path
    assert_failure
    assert_output --partial "--source-path requires a value"
}

@test "args: unknown flag rejected" {
    run "$SCRIPT" restore --serial ABC --mount-path /mnt --bogus val
    assert_failure
    assert_output --partial "Unknown argument: --bogus"
}

@test "args: cleanup without --mount-path" {
    run "$SCRIPT" cleanup
    assert_failure
    assert_output --partial "--mount-path is required"
}

@test "args: cleanup mode succeeds with --mount-path" {
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Cleanup of $TEST_MOUNT_DIR completed"
}

# =============================================================================
# Cleanup mode
# =============================================================================

@test "cleanup: calls sync then umount" {
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert [ -f "$MOCK_CALL_LOG" ]
    grep -q "^sync" "$MOCK_CALL_LOG"
    grep -q "^umount $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
}

@test "cleanup: uses lazy umount as fallback" {
    export MOCK_UMOUNT_EXIT=1
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "^umount $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
    grep -q "^umount -l $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
}

@test "cleanup: warns when lazy umount also fails" {
    export MOCK_UMOUNT_EXIT=1
    export MOCK_UMOUNT_LAZY_EXIT=1
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "^umount $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
    grep -q "^umount -l $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
    assert_output --partial "Lazy unmount of $TEST_MOUNT_DIR also failed"
}

# =============================================================================
# Sourcing guard
# =============================================================================

@test "sourcing: FILERESTORE_SOURCED=1 allows sourcing without execution" {
    FILERESTORE_SOURCED=1 run bash -c "source '$SCRIPT' && type unmount_and_cleanup"
    assert_success
    assert_output --partial "unmount_and_cleanup"
}

# =============================================================================
# Device discovery
# =============================================================================

@test "device: found by serial number" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Found device: /dev/sdb"
}

@test "device: not found by serial exits 1" {
    export MOCK_LSBLK_SERIAL_OUTPUT=""
    run "$SCRIPT" restore --serial NOTFOUND --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "Device with serial NOTFOUND not found"
}

@test "device: whole disk with partitioned filesystem" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_LSBLK_FSTYPE_OUTPUT="sdb1 ext4"
    export MOCK_BLKID_DEVICES="/dev/sdb=;/dev/sdb1=ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Device is partitioned, using partition: /dev/sdb1"
}

@test "device: partition found but blkid cannot determine fstype exits 1" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_LSBLK_FSTYPE_OUTPUT="sdb1 ext4"
    export MOCK_BLKID_DEVICES="/dev/sdb=;/dev/sdb1="
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "Could not determine filesystem type for partition"
}

@test "device: whole disk, no partition has filesystem exits 1" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT=""
    export MOCK_BLKID_EXIT=1
    export MOCK_LSBLK_FSTYPE_OUTPUT=""
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "No mountable filesystem found"
}

@test "device: direct filesystem on device" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="xfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Found device: /dev/sdb"
}

# =============================================================================
# Mount option selection
# =============================================================================

@test "mount-opts: ext4 uses ro,noload" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro,noload"
    grep -q "mount -o ro,noload" "$MOCK_CALL_LOG"
}

@test "mount-opts: ext3 uses ro,noload" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext3"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro,noload"
}

@test "mount-opts: xfs uses ro,norecovery,nouuid" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="xfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro,norecovery,nouuid"
    grep -q "mount -o ro,norecovery,nouuid" "$MOCK_CALL_LOG"
}

@test "mount-opts: other filesystem uses ro only" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ntfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro"
    grep -q "mount -o ro /dev/sdb" "$MOCK_CALL_LOG"
}

# =============================================================================
# Mount failure
# =============================================================================

@test "mount: failure exits 1" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    export MOCK_MOUNT_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "Failed to mount /dev/sdb"
}

# =============================================================================
# Manual mode
# =============================================================================

@test "manual: no --source-path exits 0 after mount" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Volume mounted at $TEST_MOUNT_DIR for manual restore operations"
}

@test "manual: volume is not unmounted after mount" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    ! grep -q "^umount" "$MOCK_CALL_LOG"
    ! grep -q "^sync" "$MOCK_CALL_LOG"
}

# =============================================================================
# Automatic mode
# =============================================================================

@test "auto: source path not found exits 1" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "does not exist on the source volume"
}

@test "auto: source path not found triggers cleanup" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    grep -q "^sync" "$MOCK_CALL_LOG"
    grep -q "^umount" "$MOCK_CALL_LOG"
}

@test "auto: rsync succeeds exits 0" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    mkdir -p "$TEST_MOUNT_DIR/./data"
    touch "$TEST_MOUNT_DIR/./data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    assert_output --partial "Automatic restore of /data/file.txt completed successfully"
}

@test "auto: rsync called with correct arguments" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    mkdir -p "$TEST_MOUNT_DIR/./data"
    touch "$TEST_MOUNT_DIR/./data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    grep -q "rsync -avR $TEST_MOUNT_DIR/./data/file.txt /" "$MOCK_CALL_LOG"
}

@test "auto: rsync failure exits 1" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    export MOCK_RSYNC_EXIT=1
    mkdir -p "$TEST_MOUNT_DIR/./data"
    touch "$TEST_MOUNT_DIR/./data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "Failed to restore /data/file.txt from source volume"
}

@test "auto: rsync failure triggers cleanup" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    export MOCK_RSYNC_EXIT=1
    mkdir -p "$TEST_MOUNT_DIR/./data"
    touch "$TEST_MOUNT_DIR/./data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    grep -q "^sync" "$MOCK_CALL_LOG"
    grep -q "^umount" "$MOCK_CALL_LOG"
}

@test "auto: successful restore triggers cleanup" {
    export MOCK_LSBLK_SERIAL_OUTPUT="sdb ABC123"
    export MOCK_BLKID_OUTPUT="ext4"
    mkdir -p "$TEST_MOUNT_DIR/./data"
    touch "$TEST_MOUNT_DIR/./data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    grep -q "^sync" "$MOCK_CALL_LOG"
    grep -q "^umount" "$MOCK_CALL_LOG"
}
