#!/bin/bash
# Automated release script for TokenHub
# Usage: ./scripts/release.sh [major|minor|patch]
# Batch mode: BATCH=yes ./scripts/release.sh [major|minor|patch]

set -e

BATCH_MODE="${BATCH:-no}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()    { echo -e "${BLUE}  $1${NC}"; }
success() { echo -e "${GREEN}  $1${NC}"; }
warn()    { echo -e "${YELLOW}  $1${NC}"; }
error()   { echo -e "${RED}  $1${NC}"; exit 1; }

check_prerequisites() {
    info "Checking prerequisites..."

    if ! command -v gh &> /dev/null; then
        error "GitHub CLI (gh) is not installed. Install with: brew install gh"
    fi

    if ! gh auth status &> /dev/null; then
        error "GitHub CLI is not authenticated. Run: gh auth login"
    fi

    if [[ -n $(git status --porcelain) ]]; then
        error "Git working directory is not clean. Commit or stash changes first."
    fi

    CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
    if [[ "$CURRENT_BRANCH" != "main" ]]; then
        warn "Not on main branch (currently on: $CURRENT_BRANCH)"
        if [[ "$BATCH_MODE" == "yes" ]]; then
            error "Not on main branch in batch mode. Switch to main first."
        fi
        read -p "Continue anyway? (y/n) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            error "Aborted by user"
        fi
    fi

    success "Prerequisites check passed"
}

get_current_version() {
    git tag -l 'v*' | sort -V | tail -1 | sed 's/^v//'
}

calculate_next_version() {
    local current=$1
    local bump_type=$2

    IFS='.' read -r major minor patch <<< "$current"

    case $bump_type in
        major) major=$((major + 1)); minor=0; patch=0 ;;
        minor) minor=$((minor + 1)); patch=0 ;;
        patch) patch=$((patch + 1)) ;;
        *) error "Invalid bump type: $bump_type (use major, minor, or patch)" ;;
    esac

    echo "$major.$minor.$patch"
}

# Generate changelog entry from git log
generate_changelog_entry() {
    local prev_version=$1
    local new_version=$2
    local date=$(date +%Y-%m-%d)

    info "Generating changelog from v$prev_version to HEAD..." >&2

    # Get commits since last version (handle first-release case)
    local commits
    if git rev-parse "v$prev_version" &>/dev/null; then
        commits=$(git log "v$prev_version"..HEAD --pretty=format:"%h %s" --no-merges)
    else
        commits=$(git log --pretty=format:"%h %s" --no-merges)
    fi

    # Categorize commits
    local added=""
    local changed=""
    local fixed=""

    while IFS= read -r line; do
        if [[ $line =~ ^[a-f0-9]+\ feat(\(.*\))?:\ (.*) ]]; then
            added+="- ${BASH_REMATCH[2]}\n"
        elif [[ $line =~ ^[a-f0-9]+\ fix(\(.*\))?:\ (.*) ]]; then
            fixed+="- ${BASH_REMATCH[2]}\n"
        elif [[ $line =~ ^[a-f0-9]+\ refactor(\(.*\))?:\ (.*) ]]; then
            changed+="- ${BASH_REMATCH[2]}\n"
        fi
    done <<< "$commits"

    # Build changelog entry
    local entry="## [$new_version] - $date\n\n"

    if [[ -n "$added" ]]; then
        entry+="### Added\n$added\n"
    fi

    if [[ -n "$changed" ]]; then
        entry+="### Changed\n$changed\n"
    fi

    if [[ -n "$fixed" ]]; then
        entry+="### Fixed\n$fixed\n"
    fi

    echo -e "$entry"
}

# Update CHANGELOG.md
update_changelog() {
    local changelog_entry=$1
    local changelog_file="CHANGELOG.md"

    if [[ ! -f "$changelog_file" ]]; then
        warn "CHANGELOG.md not found, skipping changelog update"
        warn "Create CHANGELOG.md with '## [Unreleased]' section to enable changelog generation"
        return
    fi

    info "Updating $changelog_file..."

    # Create temp files
    local temp_file=$(mktemp)
    local entry_file=$(mktemp)

    # Write the entry to a file (handles multi-line strings)
    echo -e "$changelog_entry" > "$entry_file"

    # Read changelog and insert new entry after ## [Unreleased]
    awk '
        /^## \[Unreleased\]/ {
            print $0
            print ""
            while ((getline line < "'"$entry_file"'") > 0) {
                print line
            }
            close("'"$entry_file"'")
            next
        }
        { print }
    ' "$changelog_file" > "$temp_file"

    mv "$temp_file" "$changelog_file"
    rm -f "$entry_file"

    success "CHANGELOG.md updated"
}

create_release() {
    local version=$1
    local prev_tag=$2
    local test_status=$3

    info "Creating release v$version..."

    # Generate release notes from commits
    local release_notes
    if [[ -n "$prev_tag" ]]; then
        release_notes=$(git log "$prev_tag"..HEAD --pretty=format:"- %s" --no-merges)
        local commit_count=$(git rev-list --count "$prev_tag"..HEAD)
        local compare_base="$prev_tag"
    else
        release_notes=$(git log --pretty=format:"- %s" --no-merges)
        local commit_count=$(git rev-list --count HEAD)
        local compare_base=$(git rev-list --max-parents=0 HEAD | head -1)
    fi

    cat > /tmp/tokenhub_release_notes.md << EOF
## TokenHub v$version

### Statistics
- **Commits**: $commit_count
- **Test Status**: $test_status

### Changes

$release_notes

### Downloads

Pre-built binaries are available for multiple platforms:
- **macOS (Intel x86-64)**: \`tokenhub-v${version}-darwin-amd64\`, \`tokenhubctl-v${version}-darwin-amd64\`
- **macOS (Apple Silicon ARM64)**: \`tokenhub-v${version}-darwin-arm64\`, \`tokenhubctl-v${version}-darwin-arm64\`
- **Linux (x86-64)**: \`tokenhub-v${version}-linux-amd64\`, \`tokenhubctl-v${version}-linux-amd64\`
- **Linux (ARM64)**: \`tokenhub-v${version}-linux-arm64\`, \`tokenhubctl-v${version}-linux-arm64\`

Binaries are automatically built and uploaded by GitHub Actions. Check back in a few minutes if they're not yet available.

### Installation

Extract the binary and make it executable:
\`\`\`bash
tar xzf tokenhub-v${version}-<platform>.tar.gz
sudo mv tokenhub /usr/local/bin/
sudo mv tokenhubctl /usr/local/bin/
\`\`\`

### Links
- [Full Changelog](https://github.com/jordanhubbard/tokenhub/compare/$compare_base...v$version)

---

**Full Changelog**: https://github.com/jordanhubbard/tokenhub/compare/$compare_base...v$version
EOF

    # Commit changelog if CHANGELOG.md exists
    if [[ -f CHANGELOG.md ]]; then
        info "Committing CHANGELOG.md..."
        git add CHANGELOG.md
        if ! git diff --cached --quiet; then
            git commit -m "docs: Update CHANGELOG for v$version release"
        fi
    fi

    # Create annotated git tag
    info "Creating git tag v$version..."
    git tag -a "v$version" -m "Release v$version"

    # Push commits and tag
    info "Pushing changes to origin..."
    git push origin main
    git push origin "v$version"

    # Create GitHub release
    info "Creating GitHub release..."
    gh release create "v$version" \
        --title "v$version" \
        --notes-file /tmp/tokenhub_release_notes.md

    rm -f /tmp/tokenhub_release_notes.md

    success "Release v$version created successfully!"
}

main() {
    echo ""
    echo "========================================="
    echo "   TokenHub Automated Release Script"
    echo "========================================="
    echo ""

    if [[ "$BATCH_MODE" == "yes" ]]; then
        info "Running in BATCH mode (non-interactive)"
    fi

    check_prerequisites

    CURRENT_VERSION=$(get_current_version)
    BUMP_TYPE=${1:-patch}

    if [[ ! "$BUMP_TYPE" =~ ^(major|minor|patch)$ ]]; then
        error "Invalid argument: $BUMP_TYPE (use major, minor, or patch)"
    fi

    if [[ -z "$CURRENT_VERSION" ]]; then
        # First release — use version from bump type or default to 0.1.0
        case $BUMP_TYPE in
            major) NEXT_VERSION="1.0.0" ;;
            minor) NEXT_VERSION="0.1.0" ;;
            patch) NEXT_VERSION="0.0.1" ;;
        esac
        PREV_TAG=""
        info "No existing version tags found. Creating initial release."
    else
        NEXT_VERSION=$(calculate_next_version "$CURRENT_VERSION" "$BUMP_TYPE")
        PREV_TAG="v$CURRENT_VERSION"
        info "Current version: v$CURRENT_VERSION"
    fi

    echo ""
    echo "-----------------------------------------"
    if [[ -n "$PREV_TAG" ]]; then
        echo "  Current: v$CURRENT_VERSION"
    else
        echo "  Current: (none)"
    fi
    echo "  Next:    v$NEXT_VERSION ($BUMP_TYPE)"
    echo "-----------------------------------------"
    echo ""

    if [[ "$BATCH_MODE" == "yes" ]]; then
        info "Batch mode: proceeding with release v$NEXT_VERSION"
    else
        read -p "Proceed with release v$NEXT_VERSION? (y/n) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            warn "Release cancelled by user"
            exit 0
        fi
    fi

    # Generate and apply changelog if CHANGELOG.md exists
    if [[ -f CHANGELOG.md ]]; then
        echo ""
        CHANGELOG_ENTRY=$(generate_changelog_entry "$CURRENT_VERSION" "$NEXT_VERSION")
        info "Generated changelog entry:"
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        echo -e "$CHANGELOG_ENTRY"
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        update_changelog "$CHANGELOG_ENTRY"
        echo ""
    fi

    # Run tests
    info "Running tests..."
    local test_output_file=$(mktemp)
    if ! CGO_ENABLED=1 go test -race ./... > "$test_output_file" 2>&1; then
        if [[ "$BATCH_MODE" == "yes" ]]; then
            rm -f "$test_output_file"
            error "Tests failed in batch mode. Fix tests before releasing."
        fi
        warn "Tests failed! Continue anyway?"
        read -p "(y/n) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            rm -f "$test_output_file"
            error "Release cancelled due to test failures"
        fi
    fi
    success "Tests passed"

    local test_status=$(grep -cE '^ok' "$test_output_file" || echo "0")
    test_status="$test_status packages passed"
    rm -f "$test_output_file"

    # Build Docker image
    info "Building Docker image..."
    make -C "$(git rev-parse --show-toplevel)" package VERSION="v$NEXT_VERSION"
    success "Docker image tokenhub:v$NEXT_VERSION built and tagged as latest"

    # Create release
    create_release "$NEXT_VERSION" "$PREV_TAG" "$test_status"

    echo ""
    echo "========================================="
    echo "   Release Complete!"
    echo "========================================="
    echo ""
    echo "Release: https://github.com/jordanhubbard/tokenhub/releases/tag/v$NEXT_VERSION"
    echo "Docker:  tokenhub:v$NEXT_VERSION"
    echo ""
    echo "Next: GitHub Actions will build and upload multi-platform binaries."
    echo "      Check release assets in a few minutes:"
    echo "      https://github.com/jordanhubbard/tokenhub/releases/tag/v$NEXT_VERSION"
    echo ""
}

main "$@"
