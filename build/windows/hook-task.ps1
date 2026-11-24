# Register / remove the machine-wide 'Obot Sentry Hook Install' scheduled task.
# Run by the MSI's custom actions (SYSTEM). The task runs hook-install with an
# elevated SYSTEM token so it can converge machine policy and every user
# profile's hook files.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -NoProfile -File hook-task.ps1 -Mode install -ExePath "C:\Program Files\Obot\obot-sentry\obot-sentry.exe"
#   powershell -ExecutionPolicy Bypass -NoProfile -File hook-task.ps1 -Mode uninstall

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('install', 'uninstall')][string]$Mode,
    [string]$ExePath
)

$ErrorActionPreference = 'Stop'
$TaskName = 'Obot Sentry Hook Install'

try {
    if ($Mode -eq 'uninstall') {
        # Unregistering does not stop a run in progress, and a running
        # hook-install holds its log open, which would make the removal below
        # fail. Stop it first.
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        # hook-install keeps its log and schedule state beside this script.
        # The MSI did not install them, so remove them here or the install
        # directory cannot be removed. Retry briefly in case a stopped run is
        # still releasing its handles.
        $runDir = Join-Path $PSScriptRoot 'hook-install'
        for ($i = 0; $i -lt 10 -and (Test-Path -LiteralPath $runDir); $i++) {
            Remove-Item -LiteralPath $runDir -Recurse -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $runDir) { Start-Sleep -Milliseconds 500 }
        }
        # The MSI writes HookIntervalMinutes from a Permanent component so
        # upgrades keep it; a true uninstall (the only time this mode runs)
        # removes it here.
        Remove-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Obot\obot-sentry' -Name 'HookIntervalMinutes' -ErrorAction SilentlyContinue
        exit 0
    }

    if (-not $ExePath) { throw '-ExePath is required for install' }
    if (-not (Test-Path -LiteralPath $ExePath -PathType Leaf)) {
        throw "obot-sentry.exe not found at $ExePath"
    }

    # --if-due throttles the frequent triggers below to the configured
    # HookIntervalMinutes (default 15); see the trigger comment.
    $action = New-ScheduledTaskAction -Execute $ExePath -Argument 'hook-install --if-due'

    # The hook installer needs elevation to write machine policy and every
    # user's files. SYSTEM is used instead of the per-user Users
    # principal used by the scan task; hook-install rejects a limited token.
    # Resolve the well-known SYSTEM SID to its (possibly localized) account name
    # so -UserId is locale-independent, exactly as scan-task.ps1 does for the
    # Users group: the literal 'SYSTEM' is an English display name and does not
    # resolve on every localized Windows install.
    $systemAccount = ([System.Security.Principal.SecurityIdentifier]'S-1-5-18').
    Translate([System.Security.Principal.NTAccount]).Value
    $principal = New-ScheduledTaskPrincipal -UserId $systemAccount -LogonType ServiceAccount -RunLevel Highest

    # Reconcile at startup, at logon, and on a five-minute poll. hook-install
    # converges machine policy and every user profile on the machine, signed in
    # or not, so it does not need anyone at the console. Each trigger runs it
    # with --if-due, which converges only when HookIntervalMinutes (default 15)
    # has elapsed, the machine restarted, a user profile appeared or went away,
    # the enforcement setting changed, or obot-sentry was upgraded; otherwise
    # it exits without touching a file. The logon trigger's delay lets a
    # first sign-in finish creating the new profile, which then counts as a
    # profile change. StartWhenAvailable also lets Task Scheduler catch up
    # after sleep; IgnoreNew prevents overlapping convergence. Changes and
    # failures are logged to hook-install\hook-install.log beside this
    # script, readable by SYSTEM and Administrators only.
    $bootTrigger = New-ScheduledTaskTrigger -AtStartup
    $logonTrigger = New-ScheduledTaskTrigger -AtLogOn
    $logonTrigger.Delay = 'PT1M'
    $clockTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) `
        -RepetitionInterval (New-TimeSpan -Minutes 5)
    $triggers = @($bootTrigger, $logonTrigger, $clockTrigger)
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit (New-TimeSpan -Minutes 30)

    Register-ScheduledTask -TaskName $TaskName -Force `
        -Description 'Converge Obot Sentry local-agent audit hooks' `
        -Action $action -Trigger $triggers -Principal $principal -Settings $settings | Out-Null

    # Best-effort first convergence so hooks are available immediately after
    # installation. A fresh install has no schedule state and an upgrade
    # changes the recorded version, so --if-due converges on this run.
    try { Start-ScheduledTask -TaskName $TaskName } catch { }

    exit 0
}
catch {
    Write-Error $_
    exit 1
}
