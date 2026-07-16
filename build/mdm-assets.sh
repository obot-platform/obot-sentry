#!/usr/bin/env bash
# Assemble the MDM assets tree obot serves (dist/mdm-assets): complete the
# authored build/manifest.json for the given version, sanity-check it,
# and stage every referenced file at its manifest path — source-tree
# files from build/, built installers from dist/. Run the platform
# packaging scripts first (CI does; see .github/workflows/build.yaml).
#
# Usage: build/mdm-assets.sh <version>   # numeric x.y.z
set -euo pipefail

version="${1:?usage: mdm-assets.sh <version> (numeric x.y.z)}"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "version must be numeric x.y.z (got '$version')" >&2
	exit 1
fi

buildDir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repoRoot="$(dirname "$buildDir")"
distDir="$repoRoot/dist"
outDir="$distDir/mdm-assets"

rm -rf "$outDir"
mkdir -p "$outDir"

# Complete the authored manifest (${VERSION} tokens).
sed "s/\${VERSION}/$version/g" "$buildDir/manifest.json" >"$outDir/manifest.json"

# Platform ids are unique and every configuration references one.
duplicateIDs="$(jq -r '.platforms | group_by(.id)[] | select(length > 1) | .[0].id' "$outDir/manifest.json")"
if [[ -n "$duplicateIDs" ]]; then
	echo "duplicate platform ids: $duplicateIDs" >&2
	exit 1
fi
danglingPlatforms="$(jq -r '(.platforms | map(.id)) as $ids | .configurations[] | select(.platform as $p | $ids | index($p) | not) | .platform' "$outDir/manifest.json")"
if [[ -n "$danglingPlatforms" ]]; then
	echo "configurations reference undeclared platforms: $danglingPlatforms" >&2
	exit 1
fi

# Each (platform, os) pair is one downloadable unit and must be unique.
duplicatePairs="$(jq -r '.configurations | group_by(.platform + "/" + .os)[] | select(length > 1) | .[0].platform + "/" + .[0].os' "$outDir/manifest.json")"
if [[ -n "$duplicatePairs" ]]; then
	echo "duplicate configurations: $duplicatePairs" >&2
	exit 1
fi

# The instructions template is shown in obot AND ships in the download,
# so it must be part of the configuration's assets.
missingInstructions="$(jq -r '.configurations[] | .instructions as $tmpl | select(.assets | index($tmpl) | not) | .platform + "/" + .os' "$outDir/manifest.json")"
if [[ -n "$missingInstructions" ]]; then
	echo "configurations whose instructions template is not listed in assets: $missingInstructions" >&2
	exit 1
fi

# Asset paths must stay inside the assets tree: obot joins them onto
# the directory verbatim, so refuse absolute paths and ".." segments.
unsafePaths="$(jq -r '.configurations[].assets[], (.platforms[].icon // empty)
	| select(startswith("/") or ((split("/") | index("..")) != null))' "$outDir/manifest.json")"
if [[ -n "$unsafePaths" ]]; then
	echo "unsafe asset paths (absolute or containing ..): $unsafePaths" >&2
	exit 1
fi

# Downloads are flat zips, so basenames must be unique within each
# configuration's assets.
duplicates="$(jq -r '.configurations[].assets | map(split("/") | last) | group_by(.)[] | select(length > 1) | .[0]' "$outDir/manifest.json")"
if [[ -n "$duplicates" ]]; then
	echo "duplicate basenames within a configuration: $duplicates" >&2
	exit 1
fi

# Stage every referenced file — configuration assets and platform icons —
# at its manifest-relative path.
while IFS= read -r asset; do
	destination="$outDir/$asset"
	mkdir -p "$(dirname "$destination")"
	if [[ -f "$buildDir/$asset" ]]; then
		cp "$buildDir/$asset" "$destination"
	elif [[ -f "$distDir/$(basename "$asset")" ]]; then
		cp "$distDir/$(basename "$asset")" "$destination"
	else
		echo "missing asset: $asset (not in build/$asset or dist/$(basename "$asset"))" >&2
		exit 1
	fi
done < <(jq -r '.configurations[].assets[], (.platforms[].icon // empty)' "$outDir/manifest.json" | sort -u)

echo "Staged $(find "$outDir" -type f | wc -l | tr -d ' ') files in $outDir"
