#!/usr/bin/env bash
# Build the macOS installer (dist/obot-sentry-<version>.pkg): a
# distribution-style flat package that lays down the universal binary at
# /usr/local/bin/obot-sentry plus the scan LaunchAgent and hook-install
# LaunchDaemon, and bootstraps both via the postinstall script. The file
# name carries the version because Jamf keys packages by filename: it
# refuses a second upload with an existing name, and replacing the file
# on an existing package trips its checksum recalculation, so each
# release must coexist as a distinct package. The version also lives in
# the package metadata, which MDMs compare against the device's
# `pkgutil --pkg-info ai.obot.obot-sentry` receipt for upgrades.
#
# Runs on macOS: pkgbuild, productbuild, productsign, and notarytool all
# ship with the OS or Xcode, so no extra tooling is needed.
#
# Phases gate independently so every environment produces something:
#   assemble  always
#   sign      when INSTALLER_SIGN_P12 holds the signing p12 (a path or its
#             base64 contents, as quill takes it). It must hold a Developer
#             ID Installer identity and the intermediate that issued it,
#             legacy-encoded - README.md has the recipe. MDMs refuse
#             unsigned pkgs.
#   notarize  when signing ran and the QUILL_NOTARY_* App Store Connect key
#             is present (the same secrets goreleaser's quill step uses).
#
# Usage: build/macos/pkg.sh <version> <binary>   # numeric x.y.z, universal binary
set -euo pipefail

version="${1:?usage: pkg.sh <version> <binary> (numeric x.y.z)}"
binary="${2:?usage: pkg.sh <version> <binary>}"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "version must be numeric x.y.z (got '$version')" >&2
	exit 1
fi
if [[ ! -f "$binary" ]]; then
	echo "binary not found: $binary" >&2
	exit 1
fi

buildDir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repoRoot="$(cd "$buildDir/../.." && pwd)"
distDir="$repoRoot/dist"
workDir="$distDir/macos-pkg"
out="$distDir/obot-sentry-$version.pkg"

# distribution.xml claims both architectures, so the binary has to carry
# both. Build one locally with:
#   VERSION=0.0.1 goreleaser build --snapshot --clean   # dist/*_darwin_all/obot-sentry
archs="$(lipo -archs "$binary" 2>/dev/null || true)"
if [[ "$archs" != *arm64* || "$archs" != *x86_64* ]]; then
	echo "binary must be universal (arm64 x86_64); got '${archs:-not a Mach-O binary}'" >&2
	exit 1
fi

rm -rf "$workDir"
mkdir -p "$workDir/payload/usr/local/bin" \
	"$workDir/payload/Library/LaunchAgents" \
	"$workDir/payload/Library/LaunchDaemons" \
	"$workDir/scripts"

# pkgbuild archives any xattr on a payload file as an AppleDouble ._*
# entry in the pkg, so stage without them.
ditto --noextattr --noqtn "$binary" "$workDir/payload/usr/local/bin/obot-sentry"
ditto --noextattr --noqtn "$buildDir/ai.obot.obot-sentry.scan.plist" \
	"$workDir/payload/Library/LaunchAgents/ai.obot.obot-sentry.scan.plist"
ditto --noextattr --noqtn "$buildDir/ai.obot.obot-sentry.hook-install.plist" \
	"$workDir/payload/Library/LaunchDaemons/ai.obot.obot-sentry.hook-install.plist"
ditto --noextattr --noqtn "$buildDir/postinstall" "$workDir/scripts/postinstall"
chmod 755 "$workDir/payload/usr/local/bin/obot-sentry" "$workDir/scripts/postinstall"
chmod 644 "$workDir/payload/Library/LaunchAgents/"*.plist \
	"$workDir/payload/Library/LaunchDaemons/"*.plist

# ditto cannot decline com.apple.provenance, which the OS pins on files
# created by some (e.g. sandboxed) processes. Strip what the OS allows and
# surface the rest, which ship as AppleDouble sidecars.
xattr -cr "$workDir/payload" 2>/dev/null || true
leftovers="$(xattr -lr "$workDir/payload" 2>/dev/null | cut -d: -f1 | sort -u)"
if [[ -n "$leftovers" ]]; then
	echo "warning: extended attributes survive on (payload will carry AppleDouble sidecars):" >&2
	echo "$leftovers" >&2
fi

# --ownership recommended records root:wheel for the system locations
# instead of the user that staged the payload.
pkgbuild \
	--root "$workDir/payload" \
	--identifier ai.obot.obot-sentry \
	--version "$version" \
	--install-location / \
	--scripts "$workDir/scripts" \
	--ownership recommended \
	"$workDir/component.pkg"

sed "s/\${VERSION}/$version/g" "$buildDir/distribution.xml" >"$workDir/distribution.xml"
productbuild \
	--distribution "$workDir/distribution.xml" \
	--package-path "$workDir" \
	"$workDir/obot-sentry-unsigned.pkg"

# Credentials that have to reach a tool as a file are staged here.
# mktemp -d gives 0700, and cleanup removes it however the script exits.
secretsDir="$(mktemp -d)"
keychain="$secretsDir/signing.keychain-db"
cleanup() {
	if [[ -s "$secretsDir/keychain-search-list" ]]; then
		xargs security list-keychains -d user -s <"$secretsDir/keychain-search-list"
	fi
	security delete-keychain "$keychain" 2>/dev/null || true
	rm -rf "$secretsDir"
}
trap cleanup EXIT

signed=false
if [[ -n "${INSTALLER_SIGN_P12:-}" ]]; then
	# Same contract as quill's env var: either a path to the p12 or its
	# base64 contents.
	if [[ -f "$INSTALLER_SIGN_P12" ]]; then
		cp "$INSTALLER_SIGN_P12" "$secretsDir/signing.p12"
	else
		base64 --decode <<<"$INSTALLER_SIGN_P12" >"$secretsDir/signing.p12"
	fi

	# productsign signs out of a keychain, so the identity goes into a
	# throwaway one. -T grants productsign use of the key and
	# set-key-partition-list applies that ACL, without which signing stops
	# on a GUI prompt. `security import` verifies SHA-1 MACs only, so the
	# p12 must be legacy-encoded, and it takes the password as an argument,
	# so the password is in the process list for the duration of the import.
	keychainPassword="$(uuidgen)"
	security create-keychain -p "$keychainPassword" "$keychain"
	security set-keychain-settings "$keychain"
	security unlock-keychain -p "$keychainPassword" "$keychain"
	security import "$secretsDir/signing.p12" -k "$keychain" -f pkcs12 \
		-P "${INSTALLER_SIGN_PASSWORD:-}" -T /usr/bin/productsign
	security set-key-partition-list -S apple-tool:,apple: -s \
		-k "$keychainPassword" "$keychain" >/dev/null

	# productsign signs only with an identity that passes trust evaluation,
	# and that walks the keychain search list rather than the keychain named
	# by --keychain. cleanup() puts the original list back.
	security list-keychains -d user >"$secretsDir/keychain-search-list"
	xargs security list-keychains -d user -s "$keychain" \
		<"$secretsDir/keychain-search-list"

	# MDMs install only Installer-signed packages.
	identity="$(security find-identity -v "$keychain" |
		awk -F'"' '/Developer ID Installer/ { print $2; exit }')"
	if [[ -z "$identity" ]]; then
		echo "INSTALLER_SIGN_P12 holds no valid Developer ID Installer identity:" >&2
		security find-identity "$keychain" >&2
		exit 1
	fi

	# Timestamping, which notarization requires, is on by default for
	# Developer ID identities.
	productsign --sign "$identity" --keychain "$keychain" \
		"$workDir/obot-sentry-unsigned.pkg" "$out"
	signed=true

	signature="$(pkgutil --check-signature "$out")"
	if ! grep -q "Developer ID Installer" <<<"$signature" ||
		grep -qE "invalid|untrusted" <<<"$signature"; then
		echo "unexpected signature on the pkg:" >&2
		echo "$signature" >&2
		exit 1
	fi
else
	echo "INSTALLER_SIGN_P12 not set; producing an unsigned pkg (smoke tests only)" >&2
	cp "$workDir/obot-sentry-unsigned.pkg" "$out"
fi

if [[ "$signed" == true && -n "${QUILL_NOTARY_ISSUER:-}${QUILL_NOTARY_KEY_ID:-}${QUILL_NOTARY_KEY:-}" ]]; then
	: "${QUILL_NOTARY_ISSUER:?QUILL_NOTARY_ISSUER, _KEY_ID, and _KEY must all be set to notarize}"
	: "${QUILL_NOTARY_KEY_ID:?QUILL_NOTARY_ISSUER, _KEY_ID, and _KEY must all be set to notarize}"
	: "${QUILL_NOTARY_KEY:?QUILL_NOTARY_ISSUER, _KEY_ID, and _KEY must all be set to notarize}"

	# QUILL_NOTARY_KEY carries the .p8 the way goreleaser takes it: the PEM
	# itself or its base64 contents. notarytool needs the PEM on disk and
	# rejects anything else as invalidPEMDocument.
	if [[ "$QUILL_NOTARY_KEY" == *"PRIVATE KEY"* ]]; then
		printf '%s' "$QUILL_NOTARY_KEY" >"$secretsDir/key.p8"
	else
		base64 --decode <<<"$QUILL_NOTARY_KEY" >"$secretsDir/key.p8"
	fi
	if ! grep -q "PRIVATE KEY" "$secretsDir/key.p8"; then
		echo "QUILL_NOTARY_KEY is neither a PEM .p8 nor its base64 contents" >&2
		exit 1
	fi
	xcrun notarytool submit "$out" --wait \
		--issuer "$QUILL_NOTARY_ISSUER" \
		--key-id "$QUILL_NOTARY_KEY_ID" \
		--key "$secretsDir/key.p8"
	xcrun stapler staple "$out"
	xcrun stapler validate "$out"
elif [[ "$signed" == true ]]; then
	echo "QUILL_NOTARY_* not set; skipping notarization" >&2
fi

echo "Built: $out"
