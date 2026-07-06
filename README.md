# Obocop

Obocop is a command-line tool designed to be used by MDMs for device scanning and agent hook configuration.

It enrolls each machine with an [Obot](https://github.com/obot-platform/obot) server as a single device shared by all of the machine's users, and submits device scan manifests attributed per user. Inventory collection (MCP servers, skills, plugins) is currently stubbed out — scans ship an empty manifest so the enrollment and submission flow works end to end while the scan engine lands separately.

## How it works

- **One-shot CLI, no daemon.** The MDM schedules `obocop scan --submit` per user. Each run enrolls if needed and submits a manifest; without `--submit` a scan is local-only.
- **Enrollment.** Configuration (server URL + an `ode1-...` enrollment credential minted in the Obot admin UI) is pushed by the MDM. On the machine's first run, obocop generates a shared Ed25519 identity key in the machine-scoped data dir (`%PROGRAMDATA%\obot\obocop` on Windows, `/Library/Application Support/obot/obocop` on macOS; per-user fallback when unavailable) and enrolls the public key via `POST /api/mdm/enroll` (trust-on-first-use). The device ID derives from the machine ID + key fingerprint, so all users present one device — and a lost key simply mints a fresh device ID instead of a TOFU conflict. Each user's first scan re-enrolls the same identity, which is an idempotent update server-side.
- **Submission.** Every submission is authenticated with a short-lived self-signed JWT (`aud=obot/device`) verified server-side against the enrolled key; scans land via `POST /api/devices/scans`, attributed to the submitting user by the manifest's `username`.
- **Freshness marker.** After a successful submit, obocop writes an RFC3339 timestamp to `last_scan` in its per-user data dir (`%LOCALAPPDATA%\obot\obocop` on Windows, `~/Library/Application Support/obot/obocop` on macOS); MDM detection scripts can read this file to decide whether a new scan is due.

## Commands

```
obocop scan     # build + print the manifest (add --submit to enroll + upload)
obocop enroll   # explicit enrollment, for verifying a deployment's configuration
obocop version
```

## Configuration

Resolution order per key: flags > env > MDM store.

| Key | Flag | Env | Windows registry / macOS managed prefs |
|-----|------|-----|----------------------------------------|
| Server URL | `--server-url` | `OBOCOP_SERVER_URL` | `ServerURL` |
| Enrollment key | `--enrollment-key` | `OBOCOP_ENROLLMENT_KEY` | `EnrollmentKey` |
| Username override | `--username` | `OBOCOP_USERNAME` | `Username` |
| Device name override | `--device-name` | `OBOCOP_DEVICE_NAME` | `DeviceName` |

MDM stores: `HKLM\SOFTWARE\Obot\Obocop` on Windows; `/Library/Managed Preferences/com.obot.obocop.plist` (fallback `/Library/Preferences/...`) on macOS.

## Development

```
make build          # bin/obocop
make build-windows  # bin/obocop.exe (amd64 + arm64)
make test
make validate-go-code
```

Local end-to-end against a dev server:

```
OBOCOP_SERVER_URL=http://localhost:8080 OBOCOP_ENROLLMENT_KEY=ode1-... bin/obocop scan --submit
```
