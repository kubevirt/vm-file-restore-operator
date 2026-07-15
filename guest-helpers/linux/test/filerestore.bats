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
    setup_device_mock "ext4"
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

@test "cleanup: sync timeout does not block cleanup" {
    export MOCK_TIMEOUT_EXIT=124
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "sync timed out after 10s, proceeding with unmount"
    grep -q "^timeout 10 sync" "$MOCK_CALL_LOG"
    grep -q "^umount" "$MOCK_CALL_LOG"
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
    setup_device_mock "ext4"
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
    setup_device_mock ""
    export MOCK_LSBLK_FSTYPE_OUTPUT="sdb1 ext4"
    export MOCK_BLKID_DEVICES="/dev/sdb=;/dev/sdb1=ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Device is partitioned, using partition: /dev/sdb1"
}

@test "device: partition found but blkid cannot determine fstype exits 1" {
    setup_device_mock ""
    export MOCK_LSBLK_FSTYPE_OUTPUT="sdb1 ext4"
    export MOCK_BLKID_DEVICES="/dev/sdb=;/dev/sdb1="
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "Could not determine filesystem type for partition"
}

@test "device: whole disk, no partition has filesystem exits 1" {
    setup_device_mock ""
    export MOCK_BLKID_EXIT=1
    export MOCK_LSBLK_FSTYPE_OUTPUT=""
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "No mountable filesystem found"
}

@test "device: direct filesystem on device" {
    setup_device_mock "xfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Found device: /dev/sdb"
}

# =============================================================================
# Mount option selection
# =============================================================================

@test "mount-opts: ext4 uses ro,noload" {
    setup_device_mock "ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro,noload"
    grep -q "mount -o ro,noload" "$MOCK_CALL_LOG"
}

@test "mount-opts: ext3 uses ro,noload" {
    setup_device_mock "ext3"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro,noload"
}

@test "mount-opts: xfs uses ro,norecovery,nouuid" {
    setup_device_mock "xfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro,norecovery,nouuid"
    grep -q "mount -o ro,norecovery,nouuid" "$MOCK_CALL_LOG"
}

@test "mount-opts: other filesystem uses ro only" {
    setup_device_mock "ntfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro"
    grep -q "mount -o ro /dev/sdb" "$MOCK_CALL_LOG"
}

# =============================================================================
# Mount failure
# =============================================================================

@test "mount: failure exits 1" {
    setup_device_mock "ext4"
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
    setup_device_mock "ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Volume mounted at $TEST_MOUNT_DIR for manual restore operations"
}

@test "manual: volume is not unmounted after mount" {
    setup_device_mock "ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    ! grep -q "^umount" "$MOCK_CALL_LOG"
    ! grep -q "^sync" "$MOCK_CALL_LOG"
}

# =============================================================================
# Automatic mode
# =============================================================================

@test "auto: source path not found exits 1" {
    setup_device_mock "ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "does not exist on the source volume"
}

@test "auto: source path not found triggers cleanup" {
    setup_device_mock "ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    grep -q "^sync" "$MOCK_CALL_LOG"
    grep -q "^umount" "$MOCK_CALL_LOG"
}

@test "auto: rsync succeeds exits 0" {
    setup_device_mock "ext4"
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    assert_output --partial "Automatic restore of /data/file.txt completed successfully"
}

@test "auto: rsync called with correct arguments" {
    setup_device_mock "ext4"
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    grep -q "rsync -avR $TEST_MOUNT_DIR/./data/file.txt /" "$MOCK_CALL_LOG"
}

@test "auto: rsync failure exits 1" {
    setup_device_mock "ext4"
    export MOCK_RSYNC_EXIT=1
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    [ "$status" -eq 1 ]
    assert_output --partial "Failed to restore /data/file.txt from source volume"
}

@test "auto: rsync failure triggers cleanup" {
    setup_device_mock "ext4"
    export MOCK_RSYNC_EXIT=1
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    grep -q "^sync" "$MOCK_CALL_LOG"
    grep -q "^umount" "$MOCK_CALL_LOG"
}

@test "auto: successful restore triggers cleanup" {
    setup_device_mock "ext4"
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    grep -q "^sync" "$MOCK_CALL_LOG"
    grep -q "^umount" "$MOCK_CALL_LOG"
}

# =============================================================================
# LVM detection and handling
# =============================================================================

@test "lvm: LVM2_member detected triggers vgimportclone flow" {
    setup_lvm_single_lv_mock
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "LVM detected on /dev/sdb"
    grep -q "vgimportclone.*-n filerestore_ABC123 /dev/sdb" "$MOCK_CALL_LOG"
}

@test "lvm: missing lvm2 tools exits 1" {
    setup_device_mock "LVM2_member"
    # Hide vgimportclone: create a mocks copy without it and replace PATH entry
    local safe_mocks="$BATS_TEST_TMPDIR/mocks_no_lvm"
    mkdir -p "$safe_mocks"
    for f in "$BATS_TEST_DIRNAME"/mocks/*; do
        [ "$(basename "$f")" = "vgimportclone" ] && continue
        ln -s "$f" "$safe_mocks/"
    done
    export PATH="${PATH/$BATS_TEST_DIRNAME\/mocks/$safe_mocks}"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "lvm2 tools not installed"
}

@test "lvm: vgimportclone failure exits 1" {
    setup_device_mock "LVM2_member"
    export MOCK_VGIMPORTCLONE_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "vgimportclone failed"
}

@test "lvm: vgchange activation failure cleans up and exits 1" {
    setup_device_mock "LVM2_member"
    export MOCK_VGCHANGE_AY_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "Failed to activate cloned VG"
    # State file must exist before vgchange so cleanup can deactivate the VG
    grep -q "vgremove.*filerestore_ABC123" "$MOCK_CALL_LOG"
}

@test "lvm: vgchange -ay called with correct VG name" {
    setup_lvm_single_lv_mock
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "vgchange.*-ay filerestore_ABC123" "$MOCK_CALL_LOG"
}

@test "lvm: single LV mounted at MOUNT_PATH" {
    setup_lvm_single_lv_mock
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "mount -o ro,noload /dev/filerestore_ABC123/datalv $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
}

@test "lvm: multiple LVs mounted as subdirectories" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  rootlv
  homelv"
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/rootlv=ext4;/dev/filerestore_ABC123/homelv=xfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "mount -o ro,noload /dev/filerestore_ABC123/rootlv $TEST_MOUNT_DIR/rootlv" "$MOCK_CALL_LOG"
    grep -q "mount -o ro,norecovery,nouuid /dev/filerestore_ABC123/homelv $TEST_MOUNT_DIR/homelv" "$MOCK_CALL_LOG"
}

@test "lvm: LV without filesystem is skipped" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  swaplv
  datalv"
    # swaplv has no filesystem, datalv has ext4
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/swaplv=;/dev/filerestore_ABC123/datalv=ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Skipping LV swaplv"
    # mount should not be called with swaplv; datalv should be mounted (as the only mountable LV)
    ! grep -q "^mount.*filerestore_ABC123/swaplv" "$MOCK_CALL_LOG"
}

@test "lvm: xfs on LVM gets nouuid option" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  datalv"
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/datalv=xfs"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "with options: ro,norecovery,nouuid"
}

@test "lvm: state file written with VG name" {
    setup_lvm_single_lv_mock
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    [ -f "${TEST_MOUNT_DIR}.lvm_vg" ]
    [ "$(cat "${TEST_MOUNT_DIR}.lvm_vg")" = "filerestore_ABC123" ]
}

@test "lvm: all LVM commands use --devicesfile flag" {
    setup_lvm_single_lv_mock
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "vgimportclone --devicesfile" "$MOCK_CALL_LOG"
    grep -q "vgscan --devicesfile" "$MOCK_CALL_LOG"
    grep -q "vgchange --devicesfile" "$MOCK_CALL_LOG"
    grep -q "lvs --devicesfile" "$MOCK_CALL_LOG"
}

@test "lvm: lvs failure cleans up and exits 1" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "Failed to list LVs"
    grep -q "vgremove.*filerestore_ABC123" "$MOCK_CALL_LOG"
}

@test "lvm: state file written before vgchange enables cleanup on failure" {
    setup_device_mock "LVM2_member"
    export MOCK_VGCHANGE_AY_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    # Cleanup ran vgremove — proving the state file was written before vgchange
    grep -q "vgremove.*filerestore_ABC123" "$MOCK_CALL_LOG"
    # State file removed by cleanup
    [ ! -f "${TEST_MOUNT_DIR}.lvm_vg" ]
}

@test "lvm: vgscan failure logs warning but continues" {
    setup_lvm_single_lv_mock
    export MOCK_VGSCAN_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "WARNING: vgscan --cache failed"
}

@test "lvm: no mountable LVs exits 1" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  swaplv"
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/swaplv="
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "No LVs could be mounted"
}

@test "lvm: stale VG from previous run is cleaned up before vgimportclone" {
    setup_lvm_single_lv_mock
    export MOCK_VGS_EXIT=0  # VG exists (stale)
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "Stale VG filerestore_ABC123 found"
    # Stale cleanup runs before vgimportclone
    local vgs_line vgimport_line
    vgs_line=$(grep -n "^vgs" "$MOCK_CALL_LOG" | head -1 | cut -d: -f1)
    vgimport_line=$(grep -n "^vgimportclone" "$MOCK_CALL_LOG" | head -1 | cut -d: -f1)
    [ "$vgs_line" -lt "$vgimport_line" ]
}

@test "lvm: swap+data LV mounts data directly at MOUNT_PATH" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  swaplv
  datalv"
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/swaplv=;/dev/filerestore_ABC123/datalv=ext4"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_success
    # Only 1 mountable LV, so it goes directly at MOUNT_PATH (not MOUNT_PATH/datalv)
    grep -q "mount -o ro,noload /dev/filerestore_ABC123/datalv $TEST_MOUNT_DIR$" "$MOCK_CALL_LOG"
}

# =============================================================================
# LVM cleanup
# =============================================================================

@test "lvm-cleanup: reads state file and deactivates VG" {
    echo "filerestore_ABC123" > "${TEST_MOUNT_DIR}.lvm_vg"
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR"
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "vgchange.*--devicesfile.*-an filerestore_ABC123" "$MOCK_CALL_LOG"
    grep -q "vgremove.*--devicesfile.*-f filerestore_ABC123" "$MOCK_CALL_LOG"
}

@test "lvm-cleanup: state file removed after cleanup" {
    echo "filerestore_ABC123" > "${TEST_MOUNT_DIR}.lvm_vg"
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR"
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    [ ! -f "${TEST_MOUNT_DIR}.lvm_vg" ]
}

@test "lvm-cleanup: vgchange -an failure skips vgremove" {
    echo "filerestore_ABC123" > "${TEST_MOUNT_DIR}.lvm_vg"
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR"
    export MOCK_VGCHANGE_EXIT=1
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    grep -q "vgchange.*-an filerestore_ABC123" "$MOCK_CALL_LOG"
    ! grep -q "vgremove" "$MOCK_CALL_LOG"
    assert_output --partial "Failed to deactivate VG filerestore_ABC123"
    assert_output --partial "skipping removal to avoid corruption"
    assert_output --partial "completed with errors"
}

@test "lvm-cleanup: without state file uses standard unmount" {
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    grep -q "^umount $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
    ! grep -q "vgchange" "$MOCK_CALL_LOG"
    ! grep -q "vgremove" "$MOCK_CALL_LOG"
}

# =============================================================================
# LVM automatic mode
# =============================================================================

@test "lvm-auto: single LV rsync from MOUNT_PATH" {
    setup_lvm_single_lv_mock
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    grep -q "rsync -avR $TEST_MOUNT_DIR/./data/file.txt /" "$MOCK_CALL_LOG"
}

@test "lvm-auto: multi-LV finds source in correct sub-mount" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  rootlv
  homelv"
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/rootlv=ext4;/dev/filerestore_ABC123/homelv=ext4"
    # Source path is in homelv
    create_test_source_file "/home/user/file.txt" "$TEST_MOUNT_DIR/homelv"
    mkdir -p "$TEST_MOUNT_DIR/rootlv"
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR/rootlv
$TEST_MOUNT_DIR/homelv"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /home/user/file.txt
    assert_success
    grep -q "rsync -avR $TEST_MOUNT_DIR/homelv/./home/user/file.txt /" "$MOCK_CALL_LOG"
}

@test "lvm-auto: rsync failure triggers full LVM cleanup" {
    setup_lvm_single_lv_mock
    export MOCK_RSYNC_EXIT=1
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR"
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_failure
    grep -q "vgchange.*-an filerestore_ABC123" "$MOCK_CALL_LOG"
    grep -q "vgremove.*filerestore_ABC123" "$MOCK_CALL_LOG"
}

@test "lvm-auto: successful restore triggers full LVM cleanup" {
    setup_lvm_single_lv_mock
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR"
    create_test_source_file "/data/file.txt"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /data/file.txt
    assert_success
    grep -q "vgchange.*-an filerestore_ABC123" "$MOCK_CALL_LOG"
    grep -q "vgremove.*filerestore_ABC123" "$MOCK_CALL_LOG"
}

@test "lvm-cleanup: vgremove failure warns but exits non-zero" {
    echo "filerestore_ABC123" > "${TEST_MOUNT_DIR}.lvm_vg"
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR"
    export MOCK_VGREMOVE_EXIT=1
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "Failed to remove VG filerestore_ABC123"
    assert_output --partial "completed with errors"
    [ ! -f "${TEST_MOUNT_DIR}.lvm_vg" ]
}

@test "lvm-cleanup: submounts unmounted in reverse order" {
    echo "filerestore_ABC123" > "${TEST_MOUNT_DIR}.lvm_vg"
    export MOCK_FINDMNT_OUTPUT="$TEST_MOUNT_DIR/homelv
$TEST_MOUNT_DIR/rootlv"
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    # Verify reverse sort: rootlv should be unmounted before homelv
    local rootlv_line homelv_line
    rootlv_line=$(grep -n "^umount $TEST_MOUNT_DIR/rootlv" "$MOCK_CALL_LOG" | head -1 | cut -d: -f1)
    homelv_line=$(grep -n "^umount $TEST_MOUNT_DIR/homelv" "$MOCK_CALL_LOG" | head -1 | cut -d: -f1)
    [ "$rootlv_line" -lt "$homelv_line" ]
}

@test "lvm-cleanup: empty state file skips LVM cleanup" {
    > "${TEST_MOUNT_DIR}.lvm_vg"
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "exists but is empty"
    ! grep -q "vgchange" "$MOCK_CALL_LOG"
    grep -q "^umount $TEST_MOUNT_DIR" "$MOCK_CALL_LOG"
}

@test "lvm: stale VG deactivation failure aborts" {
    setup_device_mock "LVM2_member"
    export MOCK_VGS_EXIT=0
    export MOCK_VGCHANGE_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "Failed to deactivate stale VG"
}

@test "lvm: stale VG removal failure aborts" {
    setup_device_mock "LVM2_member"
    export MOCK_VGS_EXIT=0
    export MOCK_VGREMOVE_EXIT=1
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR"
    assert_failure
    assert_output --partial "Failed to remove stale VG"
}

@test "cleanup: sync failure (non-timeout) logs correct message" {
    export MOCK_TIMEOUT_EXIT=1
    run "$SCRIPT" cleanup --mount-path "$TEST_MOUNT_DIR"
    assert_success
    assert_output --partial "sync failed (exit 1), proceeding with unmount"
}

@test "lvm-auto: multi-LV source not found in any LV exits 1" {
    setup_device_mock "LVM2_member"
    export MOCK_LVS_OUTPUT="  rootlv
  homelv"
    export MOCK_BLKID_DEVICES="/dev/sdb=LVM2_member;/dev/filerestore_ABC123/rootlv=ext4;/dev/filerestore_ABC123/homelv=ext4"
    mkdir -p "$TEST_MOUNT_DIR/rootlv"
    mkdir -p "$TEST_MOUNT_DIR/homelv"
    run "$SCRIPT" restore --serial ABC123 --mount-path "$TEST_MOUNT_DIR" --source-path /nonexistent
    assert_failure
    assert_output --partial "Source path /nonexistent not found in any LV"
}
