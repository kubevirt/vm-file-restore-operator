# GitHub PR Checks Configuration

This document describes how to configure GitHub branch protection and required status checks for the vm-file-restore-operator repository.

## Required Status Checks

The repository uses a unified CI workflow (`.github/workflows/ci.yml`) that runs multiple jobs in parallel:

1. **Lint** - Code quality checks using golangci-lint
2. **Unit Tests** - Go unit tests with coverage reporting
3. **E2E Tests** - End-to-end tests on Kind cluster
4. **Build** - Verify operator binary and Docker image build
5. **Verify Manifests** - Ensure generated manifests are up to date
6. **Verify Bundle** - Ensure OLM bundle is up to date

## Setting Up Branch Protection

To configure branch protection for the `main` branch:

### Via GitHub UI

1. Go to **Settings** → **Branches** → **Add branch protection rule**

2. Configure the following settings:

   **Branch name pattern:** `main`

   ✅ **Require a pull request before merging**
   - ✅ Require approvals: 1 (or as needed)
   - ✅ Dismiss stale pull request approvals when new commits are pushed
   - ⬜ Require review from Code Owners (optional)

   ✅ **Require status checks to pass before merging**
   - ✅ Require branches to be up to date before merging
   - **Required status checks:**
     - `Lint`
     - `Unit Tests`
     - `E2E Tests`
     - `Build`
     - `Verify Manifests`
     - `Verify Bundle`

   ✅ **Require conversation resolution before merging**

   ✅ **Include administrators** (recommended for consistency)

   ⬜ **Allow force pushes** (not recommended for main)

   ⬜ **Allow deletions** (not recommended for main)

3. Click **Create** or **Save changes**

### Via GitHub CLI

```bash
# Install gh CLI if not already installed
# https://cli.github.com/

# Configure branch protection
gh api repos/kubevirt/vm-file-restore-operator/branches/main/protection \
  --method PUT \
  --field required_status_checks='{"strict":true,"contexts":["Lint","Unit Tests","E2E Tests","Build","Verify Manifests","Verify Bundle"]}' \
  --field enforce_admins=true \
  --field required_pull_request_reviews='{"required_approving_review_count":1,"dismiss_stale_reviews":true}' \
  --field restrictions=null
```

### Via Terraform (for GitOps)

```hcl
resource "github_branch_protection" "main" {
  repository_id = "kubevirt/vm-file-restore-operator"
  pattern       = "main"

  required_status_checks {
    strict   = true
    contexts = [
      "Lint",
      "Unit Tests",
      "E2E Tests",
      "Build",
      "Verify Manifests",
      "Verify Bundle"
    ]
  }

  required_pull_request_reviews {
    dismiss_stale_reviews           = true
    require_code_owner_reviews      = false
    required_approving_review_count = 1
  }

  enforce_admins = true
}
```

## CI Workflow Details

### Lint Job
- Runs `golangci-lint v2.12.2`
- Uses Go version from `go.mod`
- Caches Go modules for faster runs
- Timeout: 10 minutes

### Unit Tests Job
- Runs `make test`
- Generates coverage report (`cover.out`)
- Uploads coverage to Codecov (optional)
- Uses envtest for Kubernetes controller testing

### E2E Tests Job
- Creates Kind cluster
- Runs `make test-e2e`
- Tests operator deployment and functionality
- Cleans up disk space before running

### Build Job
- Builds operator binary with `make build`
- Builds Docker image to verify Dockerfile

### Verify Manifests Job
- Runs `make manifests`
- Fails if generated files are out of sync
- Ensures CRDs and RBAC are up to date

### Verify Bundle Job
- Runs `make bundle`
- Fails if OLM bundle is out of sync
- Ensures bundle manifests are current

## Troubleshooting

### Check fails with "manifests out of date"
```bash
make manifests
git add config/
git commit -m "chore: regenerate manifests"
```

### Check fails with "bundle out of date"
```bash
make bundle
git add bundle/
git commit -m "chore: regenerate bundle"
```

### E2E tests fail locally
```bash
# Clean up existing Kind cluster
make cleanup-test-e2e

# Run e2e tests fresh
make test-e2e
```

### Lint failures
```bash
# Run lint locally
make lint

# Auto-fix some issues
golangci-lint run --fix
```

## Coverage Reporting (Optional)

To enable Codecov coverage reporting:

1. Sign up at https://codecov.io with your GitHub account
2. Enable the repository in Codecov
3. Add `CODECOV_TOKEN` secret to GitHub repository:
   - Go to **Settings** → **Secrets and variables** → **Actions**
   - Click **New repository secret**
   - Name: `CODECOV_TOKEN`
   - Value: (copy from Codecov dashboard)

The CI workflow will automatically upload coverage reports.

## Local Testing

Before pushing, verify all checks pass locally:

```bash
# Lint
make lint

# Unit tests
make test

# Build
make build

# Verify manifests
make manifests
git diff config/  # Should show no changes

# Verify bundle
make bundle
git diff bundle/  # Should show no changes

# E2E tests (requires Kind)
make test-e2e
```

## Skipping CI (Not Recommended)

If you absolutely need to skip CI for documentation-only changes:

```bash
git commit -m "docs: update README [skip ci]"
```

**Note:** This will skip ALL checks. Not recommended for code changes.
