# Register / remove the per-user Obocop scan scheduled task.
#
# Run by the MSI's custom actions (SYSTEM). Building the task here with the
# ScheduledTasks module — instead of a static schtasks /XML file — avoids the
# UTF-16-BOM fragility and, by taking the concrete obocop.exe path, sidesteps
# the Task Scheduler unquoted-env-var-path bug (CVE-2023-21541) that hits
# %ProgramW6432%\...\obocop.exe (the space in "C:\Program Files" would be left
# unquoted).
#
# The task runs in EACH signed-in user's own session, as that user: a
# BUILTIN\Users group principal (interactive token, no stored password) with
# no UserId, fired by a logon trigger plus a repeating clock trigger. See
# Microsoft's "run for any member of a group" guidance for LogonTrigger.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -NoProfile -File scan-task.ps1 -Mode install -ExePath "C:\Program Files\Obot\Obocop\obocop.exe"
#   powershell -ExecutionPolicy Bypass -NoProfile -File scan-task.ps1 -Mode uninstall

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('install', 'uninstall')][string]$Mode,
    [string]$ExePath
)

$ErrorActionPreference = 'Stop'
$TaskName = 'Obot Obocop Scan'

try {
    if ($Mode -eq 'uninstall') {
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        exit 0
    }

    if (-not $ExePath) { throw '-ExePath is required for install' }
    if (-not (Test-Path -LiteralPath $ExePath)) { throw "obocop.exe not found at $ExePath" }

    $action = New-ScheduledTaskAction -Execute $ExePath -Argument 'scan --submit --quiet'

    # Two triggers. The clock trigger owns the cadence: anchored once at
    # registration, repeating every 5 minutes indefinitely (no Duration),
    # it fires in EVERY logged-on user's session — including sessions that
    # existed before this registration and users who lock but never log
    # out (a locked session is still a logged-on session). The logon
    # trigger just gets fresh sessions their first scan quickly instead of
    # waiting out the next clock tick. The 5-minute cadence is deliberate:
    # each run is a cheap poll — obocop throttles real submissions to the
    # ScanIntervalMinutes registry value, so admins tune the effective
    # cadence from the MDM without touching this task.
    $logonTrigger = New-ScheduledTaskTrigger -AtLogOn
    $logonTrigger.Delay = 'PT1M'
    $clockTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) `
        -RepetitionInterval (New-TimeSpan -Minutes 5)

    # Resolve the well-known Users SID to its (possibly localized) account name
    # so -GroupId is both locale-independent and definitely accepted.
    $usersGroup = ([System.Security.Principal.SecurityIdentifier]'S-1-5-32-545').
    Translate([System.Security.Principal.NTAccount]).Value
    $principal = New-ScheduledTaskPrincipal -GroupId $usersGroup -RunLevel Limited

    $settings = New-ScheduledTaskSettingsSet -MultipleInstances Parallel -StartWhenAvailable `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RunOnlyIfNetworkAvailable `
        -ExecutionTimeLimit (New-TimeSpan -Minutes 30)

    Register-ScheduledTask -TaskName $TaskName -Force `
        -Description 'Obot Obocop device scan (runs as each signed-in user)' `
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
