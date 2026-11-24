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
        # Unregistering does not stop a running instance, whose open log would
        # make the removal below fail.
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        # The MSI did not install the run directory, so it must be removed here
        # or the install directory cannot be. Retry while a stopped run releases
        # its handles.
        $runDir = Join-Path $PSScriptRoot 'hook-install'
        for ($i = 0; $i -lt 10 -and (Test-Path -LiteralPath $runDir); $i++) {
            Remove-Item -LiteralPath $runDir -Recurse -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $runDir) { Start-Sleep -Milliseconds 500 }
        }
        # The MSI writes this from a Permanent component so upgrades keep it.
        Remove-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Obot\obot-sentry' -Name 'HookIntervalMinutes' -ErrorAction SilentlyContinue
        exit 0
    }

    if (-not $ExePath) { throw '-ExePath is required for install' }
    if (-not (Test-Path -LiteralPath $ExePath -PathType Leaf)) {
        throw "obot-sentry.exe not found at $ExePath"
    }

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

    # Frequent triggers are cheap: --if-due converges only when
    # HookIntervalMinutes has elapsed or something relevant changed. The logon
    # delay lets a first sign-in finish creating the profile, which then counts
    # as a profile change. StartWhenAvailable catches up after sleep;
    # IgnoreNew prevents overlapping runs.
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

    # Best-effort first convergence. --if-due still converges here: a fresh
    # install has no schedule state and an upgrade changes the version.
    try { Start-ScheduledTask -TaskName $TaskName } catch { }

    exit 0
}
catch {
    Write-Error $_
    exit 1
}
