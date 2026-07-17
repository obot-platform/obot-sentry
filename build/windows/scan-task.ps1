# Register / remove the machine-wide 'Obot Sentry Scan' scheduled task.
# Run by the MSI's custom actions (SYSTEM). The task runs a submitting
# device scan in each signed-in user's session, as that user.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -NoProfile -File scan-task.ps1 -Mode install -ExePath "C:\Program Files\Obot\obot-sentry\obot-sentry.exe"
#   powershell -ExecutionPolicy Bypass -NoProfile -File scan-task.ps1 -Mode uninstall

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('install', 'uninstall')][string]$Mode,
    [string]$ExePath
)

$ErrorActionPreference = 'Stop'
$TaskName = 'Obot Sentry Scan'

try {
    if ($Mode -eq 'uninstall') {
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        exit 0
    }

    if (-not $ExePath) { throw '-ExePath is required for install' }
    if (-not (Test-Path -LiteralPath $ExePath)) { throw "obot-sentry.exe not found at $ExePath" }

    $action = New-ScheduledTaskAction -Execute $ExePath -Argument 'scan --submit --quiet'

    # Logon trigger: gives fresh sessions their first scan shortly after
    # sign-in instead of waiting for the next clock tick.
    $logonTrigger = New-ScheduledTaskTrigger -AtLogOn
    $logonTrigger.Delay = 'PT1M'

    # Clock trigger: anchored once at registration, then repeats every 10
    # minutes, indefinitely, in every logged-on user's session. Each run is
    # a cheap poll — obot-sentry throttles real submissions to the
    # ScanIntervalMinutes registry value, so admins tune the cadence from
    # the MDM alone.
    $clockTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) `
        -RepetitionInterval (New-TimeSpan -Minutes 10)

    # Resolve the well-known Users SID to its (possibly localized) account name
    # so -GroupId is both locale-independent and definitely accepted.
    $usersGroup = ([System.Security.Principal.SecurityIdentifier]'S-1-5-32-545').
    Translate([System.Security.Principal.NTAccount]).Value
    $principal = New-ScheduledTaskPrincipal -GroupId $usersGroup -RunLevel Limited

    # IgnoreNew: a trigger firing while a previous run (in any session) is
    # still going is skipped rather than stacking instances.
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RunOnlyIfNetworkAvailable `
        -ExecutionTimeLimit (New-TimeSpan -Minutes 30)

    Register-ScheduledTask -TaskName $TaskName -Force `
        -Description 'Obot Sentry device scan' `
        -Action $action -Trigger $logonTrigger, $clockTrigger -Principal $principal -Settings $settings | Out-Null

    # Best-effort first run so a freshly-installed device doesn't wait for the
    # next logon. Runs in the signed-in user's session; a harmless no-op when
    # nobody is signed in. Never fails the install.
    try { Start-ScheduledTask -TaskName $TaskName } catch { }

    exit 0
}
catch {
    Write-Error $_
    exit 1
}
