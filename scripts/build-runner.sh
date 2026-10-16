#!/bin/bash
# Build and sign the on-phone UI agent: mobile-next/devicekit-ios at a pinned
# commit plus runner/runner.patch. Installs nothing; `iph up` installs and starts it.
#
#   scripts/build-runner.sh TEAM_ID [BUNDLE_PREFIX]
#
# TEAM_ID: your Apple Development team (Xcode > Settings > Accounts; a free
# Apple ID works, its profiles last 7 days). BUNDLE_PREFIX makes the bundle ids
# yours (default: dev.<team id>); keep it stable or every build is a new app.
set -euo pipefail
[[ $# -ge 1 ]] || { sed -n 2,9p "$0" | sed 's/^# \{0,1\}//'; exit 2; }
TEAM=$1
PREFIX=${2:-dev.$(tr '[:upper:]' '[:lower:]' <<<"$TEAM")}
COMMIT=158bf7feae57c45dce7243b9722c310977bcaf4b
HERE=$(cd "$(dirname "$0")/.." && pwd)
SRC=${IPH_STATE:-$HOME/.cache/iphone-ops}/devicekit
DEVICE=$("$HERE/bin/iph" device)

if [[ ! -d $SRC/.git ]]; then
  git clone -q https://github.com/mobile-next/devicekit-ios "$SRC"
fi
# each build starts from a clean worktree of the pinned commit, so a rebuild
# (a free team's profile lapses after 7 days) never meets its own edits
git -C "$SRC" fetch -q origin "$COMMIT"
WT=$(mktemp -d)
trap 'git -C "$SRC" worktree remove --force "$WT" 2>/dev/null' EXIT
git -C "$SRC" worktree add -q --detach "$WT" "$COMMIT"
cd "$WT"
git apply "$HERE/runner/runner.patch"
sed -i '' "s/PRODUCT_BUNDLE_IDENTIFIER = \"com\.mobilenext\./PRODUCT_BUNDLE_IDENTIFIER = \"$PREFIX.com.mobilenext./" \
  devicekit-ios.xcodeproj/project.pbxproj

OUT=$(mktemp -d "$SRC/dd.new.XXXX")
xcodebuild build-for-testing -quiet -allowProvisioningUpdates \
  -project devicekit-ios.xcodeproj -scheme devicekit-ios \
  -destination "platform=iOS,id=$DEVICE" \
  DEVELOPMENT_TEAM="$TEAM" CODE_SIGN_STYLE=Automatic IPHONEOS_DEPLOYMENT_TARGET=15.0 -derivedDataPath "$OUT"
codesign --verify --deep --strict "$OUT/Build/Products/Debug-iphoneos/devicekit-iosUITests-Runner.app"
# swap in only a build that verified, keeping the previous one until then
cd "$SRC" && rm -rf dd.old && { [[ ! -d dd ]] || mv dd dd.old; } && mv "$OUT" dd && rm -rf dd.old
echo "built $SRC/dd for $DEVICE (bundle $PREFIX.com.mobilenext.devicekit-ios); next: iph up"
