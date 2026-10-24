#!/usr/bin/env bash
# Build the Linux release archives and publish them as a GitHub Release.
#
#   scripts/release.sh v0.2.0              # release main at origin/main
#   scripts/release.sh v0.2.0 --dry-run    # build and check only, publish nothing
#
# Options:
#   --dry-run       Build into dist and stop before tagging or uploading.
#   --skip-tests    Do not run make check and make test first.
#   --allow-branch  Release the current branch instead of requiring main.
#   --draft         Create a draft release instead of publishing it.
#
# Versions with a suffix (v0.2.0-beta.1) are marked as prereleases. Works from
# any directory. If GitHub already created the tag and release (for example from
# the web), the archives are uploaded to that release as long as the tag points
# at the commit being built.
set -euo pipefail

REPO="${ZELA_REPO:-nexusrun/zela}"
OUT=dist

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

VERSION=""
DRY_RUN=0
SKIP_TESTS=0
ALLOW_BRANCH=0
DRAFT=0
while [ $# -gt 0 ]; do
	case "$1" in
	--dry-run) DRY_RUN=1 ;;
	--skip-tests) SKIP_TESTS=1 ;;
	--allow-branch) ALLOW_BRANCH=1 ;;
	--draft) DRAFT=1 ;;
	-h | --help) sed -n '2,17p' "$0"; exit 0 ;;
	-*) die "unknown option $1" ;;
	*) VERSION="$1" ;;
	esac
	shift
done

echo "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' ||
	die "usage: scripts/release.sh vX.Y.Z [--dry-run] [--skip-tests] [--allow-branch] [--draft]"

cd "$(dirname "$0")/.."
for cmd in git go python3 node gh shasum; do
	command -v "$cmd" >/dev/null || die "$cmd is required"
done
gh auth status >/dev/null 2>&1 || die "gh is not logged in: run 'gh auth login'"

# Release exactly what is pushed: a clean tree on main, level with origin.
[ -z "$(git status --porcelain --untracked-files=no)" ] || die "commit or stash your changes first"
git fetch -q origin --tags --force
BRANCH=$(git branch --show-current)
if [ "$ALLOW_BRANCH" = 0 ]; then
	[ "$BRANCH" = "main" ] || die "switch to main first (git switch main && git pull), or pass --allow-branch"
fi
UPSTREAM=$(git rev-parse --verify -q "origin/$BRANCH" || true)
[ -n "$UPSTREAM" ] || die "push $BRANCH to origin first"
[ "$(git rev-parse HEAD)" = "$UPSTREAM" ] || die "$BRANCH differs from origin/$BRANCH: git pull or git push first"
COMMIT=$(git rev-parse HEAD)
SHORT=$(git rev-parse --short HEAD)

# A tag may exist already (GitHub creates one when a release is published from
# the web). Reuse it only if it points at the same code we are building.
TAG_COMMIT=$(git rev-parse -q --verify "refs/tags/$VERSION^{commit}" || true)
if [ -n "$TAG_COMMIT" ] && [ "$TAG_COMMIT" != "$COMMIT" ]; then
	if [ -n "$(git diff "$TAG_COMMIT" "$COMMIT")" ]; then
		die "$VERSION already points at $(git rev-parse --short "$TAG_COMMIT") with different code; choose a new version"
	fi
	log "$VERSION points at $(git rev-parse --short "$TAG_COMMIT"), which has the same code as $SHORT"
fi
ASSETS=$(gh release view "$VERSION" -R "$REPO" --json assets -q '.assets[].name' 2>/dev/null || true)
if echo "$ASSETS" | grep -q '^zela-.*\.tar\.gz$'; then
	die "release $VERSION already has archives; choose a new version"
fi

if [ "$SKIP_TESTS" = 0 ]; then
	log "Running checks and tests"
	make -s check >/dev/null || die "checks failed: run make check to see why"
	make -s test >/dev/null 2>&1 || die "tests failed: run make test to see why"
fi

log "Building Zela $VERSION ($SHORT)"
rm -rf "$OUT"
python3 scripts/release.py --version "$VERSION" --output "$OUT" >/dev/null
(cd "$OUT" && shasum -a 256 -c SHA256SUMS >/dev/null) || die "archive checksums do not match SHA256SUMS"
for arch in amd64 arm64; do
	[ -f "$OUT/zela-$VERSION-linux-$arch.tar.gz" ] || die "missing $arch archive"
	for bin in zela-server zela; do
		tar -xzOf "$OUT/zela-$VERSION-linux-$arch.tar.gz" "zela-$VERSION-linux-$arch/bin/$bin" | grep -aq "$VERSION" ||
			die "$arch $bin does not carry version $VERSION"
	done
done
ls -lh "$OUT"

if [ "$DRY_RUN" = 1 ]; then
	log "Dry run: built $OUT, nothing was tagged or uploaded"
	exit 0
fi

# Pushing the tag also starts the Release workflow, which skips releases that
# already exist, so this script and CI never publish twice.
if [ -z "$TAG_COMMIT" ]; then
	log "Tagging $VERSION"
	git tag -a "$VERSION" -m "Zela $VERSION" "$COMMIT"
	git push -q origin "$VERSION"
fi

FILES="$OUT/zela-$VERSION-linux-amd64.tar.gz $OUT/zela-$VERSION-linux-arm64.tar.gz $OUT/SHA256SUMS"
if gh release view "$VERSION" -R "$REPO" >/dev/null 2>&1; then
	log "Uploading to the existing release $VERSION"
	# shellcheck disable=SC2086 # FILES is a fixed list of paths without spaces
	gh release upload "$VERSION" -R "$REPO" $FILES --clobber
else
	log "Creating release $VERSION"
	set -- --verify-tag --title "Zela $VERSION" --generate-notes
	case "$VERSION" in *-*) set -- "$@" --prerelease ;; esac
	[ "$DRAFT" = 0 ] || set -- "$@" --draft
	# shellcheck disable=SC2086
	gh release create "$VERSION" -R "$REPO" $FILES "$@"
fi

log "Published https://github.com/$REPO/releases/tag/$VERSION"
[ "$DRAFT" = 0 ] || log "Draft: publish it on GitHub before servers can install it"
log "Install on a server: sudo bash install.sh --version $VERSION"
