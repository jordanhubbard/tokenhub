# Release Process

This document describes how to create a new release of TokenHub with multi-platform binaries.

## Prerequisites

Before releasing, ensure you have:

1. **GitHub CLI installed and authenticated**:
   ```bash
   brew install gh  # macOS
   gh auth login
   ```

2. **Working directory clean**:
   ```bash
   git status  # Should show: nothing to commit, working tree clean
   ```

3. **On main branch**:
   ```bash
   git checkout main
   git pull origin main
   ```

4. **All tests passing**:
   ```bash
   make test
   ```

## Semantic Versioning

TokenHub follows [Semantic Versioning](https://semver.org/):

- **MAJOR.MINOR.PATCH** (e.g., v1.2.3)
- **MAJOR**: Incompatible API changes
- **MINOR**: Backward-compatible new features
- **PATCH**: Backward-compatible bug fixes

## Creating a Release

### Step 1: Run the Release Script

```bash
./scripts/release.sh [major|minor|patch]
```

Example:
```bash
./scripts/release.sh minor  # Creates a minor version release
```

### Step 2: What the Script Does

The release script automates the entire release process:

1. **Validates prerequisites**
   - Checks GitHub CLI authentication
   - Ensures working directory is clean
   - Verifies you're on main branch

2. **Calculates next version**
   - Reads current version from git tags
   - Applies semantic versioning bump

3. **Generates changelog**
   - Parses commits since last release
   - Categorizes by: Added, Changed, Fixed
   - Updates CHANGELOG.md

4. **Runs tests**
   - Executes test suite: `make test`
   - Aborts if tests fail

5. **Builds Docker image**
   - Runs: `make package VERSION=v<version>`
   - Tags as `tokenhub:v<version>` and `tokenhub:latest`

6. **Creates git tag and release**
   - Creates annotated git tag
   - Pushes tag and commits to origin
   - Creates GitHub release with release notes

### Step 3: Wait for GitHub Actions

After the release is created:

1. GitHub Actions automatically builds multi-platform binaries
2. Binaries are uploaded to the GitHub release page
3. This typically takes 5-10 minutes

Check progress at: https://github.com/jordanhubbard/tokenhub/actions/workflows/release.yml

## Available Platforms

TokenHub binaries are built for all of these platforms:

| Platform | Binary |
|----------|--------|
| macOS (Intel x86-64) | `tokenhub-v*-darwin-amd64.tar.gz` |
| macOS (Apple Silicon ARM64) | `tokenhub-v*-darwin-arm64.tar.gz` |
| Linux (x86-64) | `tokenhub-v*-linux-amd64.tar.gz` |
| Linux (ARM64) | `tokenhub-v*-linux-arm64.tar.gz` |

Each release archive includes unversioned `tokenhub` and `tokenhubctl` binaries
for every platform.

## Installation

To install a TokenHub binary:

### macOS

```bash
# Download and extract
tar xzf tokenhub-v1.0.0-darwin-arm64.tar.gz

# Install to /usr/local/bin
sudo mv tokenhub /usr/local/bin/
sudo mv tokenhubctl /usr/local/bin/

# Verify
tokenhub --version
tokenhubctl --version
```

### Linux

```bash
# Download and extract
tar xzf tokenhub-v1.0.0-linux-amd64.tar.gz

# Install to /usr/local/bin
sudo mv tokenhub /usr/local/bin/
sudo mv tokenhubctl /usr/local/bin/

# Verify
tokenhub --version
tokenhubctl --version
```

### Using with Package Managers (Future)

Future releases may support:
- **Homebrew** (macOS): `brew install tokenhub`
- **apt** (Debian/Ubuntu): `apt install tokenhub`
- **Go packages**: `go install github.com/jordanhubbard/tokenhub/cmd/tokenhub@latest`

## Local Testing

To test cross-compilation locally without creating a release:

```bash
# Build all platforms
make build-all-platforms

# Or build specific platforms
make build-linux-amd64
make build-darwin-arm64
```

Binaries will be in the `bin/` directory.

## Troubleshooting

### Tests Fail Before Release

The script aborts if tests fail. Fix the failing tests:

```bash
make test  # See which tests fail
# Fix issues
git add .
git commit -m "fix: ..."
./scripts/release.sh [major|minor|patch]  # Try again
```

### GitHub CLI Not Authenticated

```bash
gh auth login  # Follow the prompt to authenticate
```

### Binaries Not Showing Up

Check GitHub Actions status:

```bash
gh run list --workflow=release.yml
```

View logs for the failed job:

```bash
gh run view <run-id> --log
```

### Rolling Back a Release

If something goes wrong:

```bash
# Delete the tag locally and on origin
git tag -d v1.2.3
git push origin --delete v1.2.3

# Delete the GitHub release
gh release delete v1.2.3 --yes
```

Then fix the issue and run the release script again.

## CI/CD Pipeline

The complete release pipeline consists of:

1. **Local Development**
   - Write code, commit with conventional commits
   - Run tests locally: `make test`

2. **Push to GitHub**
   - `git push origin main`
   - GitHub Actions CI runs: linting, tests, Docker build

3. **Create Release**
   - Run `./scripts/release.sh [major|minor|patch]`
   - Script validates, tests, builds, and creates release

4. **Automated Builds**
   - GitHub Actions `release.yml` workflow triggers on tag push
   - Builds binaries for: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
   - Uploads to GitHub release assets

5. **Distribution**
   - Users download binaries from GitHub release page
   - Install locally or via package managers (future)

## Files Modified During Release

The release process modifies these files:

- **CHANGELOG.md**: Updated with new version entry
- **.git/tags/**: New annotated git tag created

All other files remain unchanged. The release is entirely driven by git tags.

## Conventional Commits

To get proper changelog categorization, use conventional commits:

```bash
# Feature
git commit -m "feat: add user authentication"

# Bug fix
git commit -m "fix: resolve login timeout issue"

# Refactor (shows as "Changed")
git commit -m "refactor: simplify provider routing logic"

# Chore or docs (appear in "Other")
git commit -m "docs: update API documentation"
```

Commits starting with other prefixes are included in the changelog but not specially categorized.

## References

- [Semantic Versioning](https://semver.org/)
- [Keep a Changelog](https://keepachangelog.com/)
- [Conventional Commits](https://www.conventionalcommits.org/)
- [GitHub CLI Documentation](https://cli.github.com/manual/)
