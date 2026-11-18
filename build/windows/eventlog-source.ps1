# Register / remove the 'Obot Sentry' Application event log source.
# Run by the MSI's custom actions (SYSTEM). obot-sentry writes its install-time
# enrollment check and scan failures under this source; registering it gives
# those events readable messages and lets Get-WinEvent find them by name.
#
# .NET's registration points the source at EventLogMessages.dll, which renders
# every event ID as the event's own message text.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -NoProfile -File eventlog-source.ps1 -Mode install
#   powershell -ExecutionPolicy Bypass -NoProfile -File eventlog-source.ps1 -Mode uninstall

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('install', 'uninstall')][string]$Mode
)

$ErrorActionPreference = 'Stop'
$Source = 'Obot Sentry'

try {
    if ($Mode -eq 'uninstall') {
        if ([System.Diagnostics.EventLog]::SourceExists($Source)) {
            [System.Diagnostics.EventLog]::DeleteEventSource($Source)
        }
        exit 0
    }

    if (-not [System.Diagnostics.EventLog]::SourceExists($Source)) {
        [System.Diagnostics.EventLog]::CreateEventSource($Source, 'Application')
    }
    exit 0
}
catch {
    Write-Error $_
    exit 1
}
