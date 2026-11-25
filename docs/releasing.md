# Building, releasing, and rolling out

A change reaches devices in three steps. Build MDM assets from your branch and test them against a local Obot, tag a release, then update the default asset source in Obot.

## How the assets are built

CI runs `build.yaml`. A Linux job builds the Windows exe and the macOS universal binary with GoReleaser. It signs and notarizes the macOS binary with [quill](https://github.com/anchore/quill) when the `QUILL_*` secrets are available, and falls back to an ad-hoc signature on PRs and forks. A Windows runner then builds the MSI, a macOS runner builds the pkg, and `build/mdm-assets.sh` assembles `dist/mdm-assets/`.

`dist/mdm-assets/` holds the installers, the instruction templates, and `manifest.json`. In the manifest, `fields` is the JSON Schema that Obot renders the admin form from and validates values against. `configurations` lists each downloadable platform and channel with its name, description, instructions, and files. Obot renders the `*.tmpl` files and serves each configuration as a zip. Obot never renders the enrollment key into a template. The templates carry a `REPLACE_WITH_ENROLLMENT_KEY` placeholder that the admin fills in the MDM.

The Windows installers are unsigned. Intune installs them silently as SYSTEM, which does not check Authenticode signatures.

To build locally, run the Windows steps on Windows with [WiX v4](https://docs.firegiant.com/wix/) (`dotnet tool install --global wix`) and Microsoft's [Win32 Content Prep Tool](https://github.com/microsoft/Microsoft-Win32-Content-Prep-Tool):

```
build\windows\msi.ps1 -Version 1.2.3 -Exe bin\obot-sentry.exe   # dist\obot-sentry.msi
build\windows\intune\intunewin.ps1                            # dist\obot-sentry.intunewin
build/mdm-assets.sh 1.2.3                                     # dist/mdm-assets/, on any OS
```

Run the macOS step on macOS. It takes the universal binary only and needs no tools beyond the OS and Xcode:

```
build/macos/pkg.sh 1.2.3 bin/obot-sentry   # dist/obot-sentry-1.2.3.pkg
```

The pkg file name carries the version because Jamf identifies packages by file name. The MSI file name does not.

### Signing the macOS pkg

`pkg.sh` signs and notarizes only when the credentials are set, so a local run without them still produces an unsigned pkg for testing.

Signing reads `INSTALLER_SIGN_P12`, either a path to the p12 file or its base64 contents, and `INSTALLER_SIGN_PASSWORD`. The p12 must meet three requirements:

- It holds a Developer ID Installer certificate. MDMs accept only this kind, and it is separate from the Developer ID Application certificate that quill signs the binary with.
- It uses legacy encoding. `security import` verifies only SHA-1 MACs and reports any other MAC as a wrong password.
- It includes the intermediate certificate that issued the leaf. `productsign` embeds only intermediates it finds in the keychain, and macOS ships the G1 Developer ID intermediate but not G2.

To make one from the certificate and key Apple issued:

```
openssl x509 -inform DER -in developerID_installer.cer -out installer-cert.pem
openssl x509 -in installer-cert.pem -noout -issuer   # shows which intermediate to fetch
curl -sSfL https://www.apple.com/certificateauthority/DeveloperIDG2CA.cer |
  openssl x509 -inform DER -out DeveloperIDG2CA.pem
openssl pkcs12 -export -legacy -macalg sha1 -inkey privatekey.key \
  -in installer-cert.pem -certfile DeveloperIDG2CA.pem -out installer.p12
```

## 1. Test a change with real MDM assets

Push your change to a scratch branch on this repo, not your fork, and build the asset tree there:

```
git switch -c "$(git branch --show-current)-mdm"
git push upstream HEAD
MDM_REMOTE=upstream make mdm VERSION=0.1.8   # runs build.yaml and downloads dist/mdm-assets
```

The `-mdm` suffix keeps the push off `main` even if you start there. Afterward, switch back to your branch and delete the scratch branch with `git push upstream --delete <branch>-mdm`.

Only this repo holds the signing secrets. Intune and Jamf accept only a Developer ID-signed pkg, and the Full Disk Access profile applies only to a binary signed by our team. A plain `make mdm` builds on your fork without signing, as PR builds do. Use those assets, and every PR's `mdm-assets` artifact, only for the Windows channels and the manual macOS channel.

Pass a `VERSION` higher than the one on your test machine. Without it, the build is versioned `0.0.<run number>`, and the MSI refuses to downgrade a machine that runs a release.

Point a local Obot at the result with an absolute path:

```
OBOT_SERVER_MDM_ASSET_SOURCE=$PWD/dist/mdm-assets
```

Obot imports the source at startup when the value changes. A rebuild into the same folder does not change the value, so after a rebuild click Refresh on Obot's Inventory page, or call `POST /api/mdm/asset-source/refresh`. Then download a configuration from the Inventory wizard, install it on a test machine, and check that the device enrolls and submits a scan.

## 2. Release

Cut a release from `main` by pushing a `v<x.y.z>` tag to this repo:

```
git checkout main && git pull upstream main
git tag v0.1.8
git push upstream v0.1.8
```

`release.yml` runs `build.yaml` with that version and the signing secrets. It then creates a GitHub release with `mdm-assets.tar.gz` and a cosign-signed `checksums.txt`. The version must be numeric. A prerelease tag such as `v0.1.8-rc1` is skipped, because the MSI's `ProductVersion` cannot carry a suffix. Obot consumes the tarball's URL:

```
https://github.com/obot-platform/obot-sentry/releases/download/v0.1.8/mdm-assets.tar.gz
```

Before rolling it out, set `OBOT_SERVER_MDM_ASSET_SOURCE` on a local Obot to that URL and repeat the test from step 1.

## 3. Roll it out in Obot

Obot defaults `OBOT_SERVER_MDM_ASSET_SOURCE` to a pinned release URL, so a new release reaches deployments only after Obot updates that default. In [obot](https://github.com/obot-platform/obot), open a PR titled `chore: bump obot-sentry to v0.1.8` that changes the version in two places:

- the `default:` tag on `MDMAssetSource` in `pkg/services/config.go`
- the default for `OBOT_SERVER_MDM_ASSET_SOURCE` in `docs/docs/configuration/server-configuration.md`

Once an Obot release includes the change, upgraded servers import the new assets at startup and the Inventory wizard serves the new installers. Devices already deployed get the new version when admins push the new installer through their MDM. Deployments that set `OBOT_SERVER_MDM_ASSET_SOURCE` themselves, such as through the Helm chart's `config.OBOT_SERVER_MDM_ASSET_SOURCE`, must update it by hand.
