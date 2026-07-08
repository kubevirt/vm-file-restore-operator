# Guest Helper Script Tests

Unit tests for the Linux guest helper script (`filerestore.sh`).

## Quick Start

Run script tests (requires Docker or Podman):

```bash
make test-scripts
```

By default, `CONTAINER_ENGINE` inherits from `CONTAINER_TOOL`, which defaults to `docker`. To override:

```bash
CONTAINER_ENGINE=docker make test-scripts
```

## Prerequisites

Only a container runtime (Docker or Podman) is needed:

- [`bats/bats`](https://hub.docker.com/r/bats/bats) official image (includes [bats-core](https://github.com/bats-core/bats-core), [bats-support](https://github.com/bats-core/bats-support), [bats-assert](https://github.com/bats-core/bats-assert))

No local installation of BATS is required.

## Test Structure

```text
guest-helpers/linux/
  filerestore.sh
  test/
    filerestore.bats          # BATS test cases
    test_helper.bash          # Shared setup, mock defaults
    mocks/                    # Stub scripts for system commands
      lsblk, blkid, mount, umount, rsync, sync, sudo
```

## How the Tests Work

The test suite uses mock scripts that shadow real system commands via `$PATH`
prepending. Each mock reads environment variables to control its behavior
(output, exit codes) and logs calls to `$MOCK_CALL_LOG` for verification.

The production script is modified minimally for testability:
- `FILERESTORE_SKIP_ROOT_CHECK=1` bypasses the `sudo` re-exec
- `FILERESTORE_SOURCED=1` allows sourcing functions without running main logic

## Shellcheck

Run static analysis on bash scripts:

```bash
# Requires shellcheck installed locally
make shellcheck

# Or via container
docker run --rm -v $(pwd):/workspace:Z -w /workspace koalaman/shellcheck:stable \
  guest-helpers/linux/filerestore.sh guest-helpers/linux/setup.sh
```
