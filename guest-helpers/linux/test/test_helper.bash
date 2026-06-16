#!/bin/bash
# Shared setup/teardown for filerestore.sh BATS tests.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$SCRIPT_DIR/filerestore.sh"

load "${BATS_SUPPORT_HOME:-$BATS_TEST_DIRNAME/lib/bats-support}/load"
load "${BATS_ASSERT_HOME:-$BATS_TEST_DIRNAME/lib/bats-assert}/load"

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

    # Temp directory for fake mount points
    export TEST_MOUNT_DIR="$BATS_TEST_TMPDIR/mnt"
    mkdir -p "$TEST_MOUNT_DIR"
}

teardown() {
    rm -rf "$BATS_TEST_TMPDIR"/* 2>/dev/null || true
    unset SSH_ORIGINAL_COMMAND
}
