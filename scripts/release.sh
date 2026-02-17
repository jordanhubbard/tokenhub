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

### Links
- [Full Changelog](https://github.com/jordanhubbard/tokenhub/compare/$compare_base...v$version)

---

**Full Changelog**: https://github.com/jordanhubbard/tokenhub/compare/$compare_base...v$version
EOF

    # Create annotated git tag
    info "Creating git tag v$version..."
    git tag -a "v$version" -m "Release v$version"

    # Push tag
    info "Pushing tag to origin..."
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

    # Run tests
    info "Running tests..."
    local test_output_file=$(mktemp)
    if ! go test -race ./... > "$test_output_file" 2>&1; then
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
    make -C "$(git rev-parse --show-toplevel)" docker VERSION="v$NEXT_VERSION"
    docker tag "tokenhub:v$NEXT_VERSION" tokenhub:latest
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
}

main "$@"
