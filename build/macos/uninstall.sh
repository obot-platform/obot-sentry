#!/usr/bin/env bash
# Remove Obot Sentry from this device: both launchd jobs, the binary, the pkg
# receipt, any local configuration, and the device identity plus every
# user's scan state. Idempotent, and harmless on a device where Obot Sentry
# was never installed.
#
# Usage: sudo bash uninstall.sh
set -euo pipefail

if [[ "$(id -u)" != 0 ]]; then
	echo "uninstall.sh must run as root: sudo bash uninstall.sh" >&2
	exit 1
fi

# Stop convergence first, so the daemon cannot re-add the hooks removed below.
launchctl bootout system/ai.obot.obot-sentry.hook-install 2>/dev/null || true

if [[ -x /usr/local/bin/obot-sentry ]]; then
	/usr/local/bin/obot-sentry hook-uninstall || true
fi

# The scan agent runs in each signed-in user's GUI domain, so unload it per
# session. One loginwindow process per session gives the uids to visit.
while IFS= read -r uid; do
	launchctl bootout "gui/${uid}/ai.obot.obot-sentry.scan" 2>/dev/null || true
done < <(ps -axo uid= -o command= | awk '/[l]oginwindow.app/ { print $1 }' | sort -u)

rm -f /usr/local/bin/obot-sentry \
	/Library/LaunchAgents/ai.obot.obot-sentry.scan.plist \
	/Library/LaunchDaemons/ai.obot.obot-sentry.hook-install.plist
pkgutil --forget ai.obot.obot-sentry 2>/dev/null || true

# Local configuration only. MDM-delivered settings live under /Library/Managed
# Preferences and go away with the configuration profile.
defaults delete /Library/Preferences/ai.obot.obot-sentry 2>/dev/null || true
# defaults delete empties the domain but leaves the plist behind.
rm -f /Library/Preferences/ai.obot.obot-sentry.plist

rm -rf "/Library/Application Support/obot/obot-sentry"
for home in /Users/*; do
	[[ -d "$home" ]] || continue
	rm -rf "$home/Library/Application Support/obot/obot-sentry" \
		"$home/Library/Caches/obot/obot-sentry"
done

echo "Obot Sentry removed. Full Disk Access entries for /usr/local/bin/obot-sentry," \
	"if any were granted by hand, remain in System Settings."
