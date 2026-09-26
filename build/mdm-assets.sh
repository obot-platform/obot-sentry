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
mkdir -p "$outDir" "$distDir"

# ZCode is delivered as a platform-specific marketplace archive. The MDM
# download is a flat zip, so listing the individual plugin files would lose the
# directory structure ZCode's local marketplace loader requires. Use Python's
# standard library rather than depending on a platform zip executable.
rm -f "$distDir/obot-sentry-zcode-windows.zip" "$distDir/obot-sentry-zcode-macos.zip"
python3 - "$buildDir/zcode/windows" "$buildDir/zcode/macos" "$distDir" <<'PY'
import json
import pathlib
import sys
import zipfile

source_dirs = [sys.argv[1], sys.argv[2]]
dist_dir = pathlib.Path(sys.argv[3])
for platform, source in zip(("windows", "macos"), source_dirs):
    archive = dist_dir / f"obot-sentry-zcode-{platform}.zip"
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as bundle:
        root = pathlib.Path(source)
        for file in sorted(root.rglob("*")):
            if file.is_file():
                name = file.relative_to(root).as_posix()
                bundle.write(file, name)
    with zipfile.ZipFile(archive) as bundle:
        names = set(bundle.namelist())
        required = {"marketplace.json", "obot-sentry/.zcode-plugin/plugin.json", "obot-sentry/hooks/hooks.json"}
        missing = required - names
        if missing:
            raise SystemExit(f"{archive}: missing entries {sorted(missing)}")
        manifest = json.loads(bundle.read("obot-sentry/.zcode-plugin/plugin.json"))
        if "hooks" in manifest:
            raise SystemExit(f"{archive}: plugin manifest must rely on hooks/hooks.json auto-discovery")
        hooks = json.loads(bundle.read("obot-sentry/hooks/hooks.json"))["hooks"]
        for event, groups in hooks.items():
            for group in groups:
                if "matcher" in group:
                    raise SystemExit(f"{archive}: {event} has an invalid matcher")
                for hook in group["hooks"]:
                    if hook.get("timeoutMs", 0) <= 5000:
                        raise SystemExit(f"{archive}: {event} timeout is not above the Sentry budget")
                    # Two executor shapes are legitimate, and which one is correct
                    # depends on the platform's argument handling:
                    #   process - argv is passed through, correct on POSIX where
                    #             /bin/sh receives the line verbatim.
                    #   command - the whole line is handed to the system shell.
                    #             Windows needs this: `process` re-quotes each
                    #             argument for the C runtime, which escapes the
                    #             quotes cmd.exe needs around a Program Files path
                    #             and leaves cmd unable to find the helper.
                    kind = hook.get("type")
                    if kind not in {"process", "command"}:
                        raise SystemExit(f"{archive}: {event} has an unsupported executor type {kind!r}")
                    if event == "PreToolUse":
                        # Either way the launcher must reach a shell and must
                        # still map a missing or failed helper to exit 2.
                        if kind == "process":
                            if hook.get("command") not in {"cmd.exe", "/bin/sh"} or not any("|| exit" in arg for arg in hook.get("args", [])):
                                raise SystemExit(f"{archive}: PreToolUse is not fail-closed")
                        elif "|| exit" not in hook.get("command", ""):
                            raise SystemExit(f"{archive}: PreToolUse is not fail-closed")
                    elif kind == "process" and "--defer" not in hook.get("args", []):
                        raise SystemExit(f"{archive}: {event} is not deferred")
PY

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
