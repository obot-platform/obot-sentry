# Register / remove the machine-wide 'Obot Sentry Hook Install' scheduled task.
# Run by the MSI's custom actions (SYSTEM). The task runs hook-install with an
# elevated SYSTEM token so it can converge machine policy and the active
# console user's hook files.
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
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        exit 0
    }

    if (-not $ExePath) { throw '-ExePath is required for install' }
    if (-not (Test-Path -LiteralPath $ExePath -PathType Leaf)) {
        throw "obot-sentry.exe not found at $ExePath"
    }

    $action = New-ScheduledTaskAction -Execute $ExePath -Argument 'hook-install'

    # The hook installer needs elevation to write machine policy and the
    # signed-in user's files. SYSTEM is used instead of the per-user Users
    # principal used by the scan task; hook-install rejects a limited token.
    # Resolve the well-known SYSTEM SID to its (possibly localized) account name
    # so -UserId is locale-independent, exactly as scan-task.ps1 does for the
    # Users group: the literal 'SYSTEM' is an English display name and does not
    # resolve on every localized Windows install.
    $systemAccount = ([System.Security.Principal.SecurityIdentifier]'S-1-5-18').
    Translate([System.Security.Principal.NTAccount]).Value
    $principal = New-ScheduledTaskPrincipal -UserId $systemAccount -LogonType ServiceAccount -RunLevel Highest

    # Reconcile at logon and hourly. The command targets the active console
    # user, so convergence is only meaningful once someone is signed in: an
    # at-boot trigger would find no user on every boot. The logon trigger (any
    # user) covers reboots and sign-ins; the delay lets the session reach the
    # Active state with a loaded profile before hook-install queries it. The
    # hourly trigger picks up later drift and console-user switches.
    # StartWhenAvailable also lets Task Scheduler catch up after sleep; IgnoreNew
    # prevents overlapping convergence.
    $logonTrigger = New-ScheduledTaskTrigger -AtLogOn
    $logonTrigger.Delay = 'PT1M'
    $clockTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) `
        -RepetitionInterval (New-TimeSpan -Hours 1)
    $triggers = @($logonTrigger, $clockTrigger)
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit (New-TimeSpan -Minutes 30)

    Register-ScheduledTask -TaskName $TaskName -Force `
        -Description 'Converge Obot Sentry local-agent audit hooks' `
        -Action $action -Trigger $triggers -Principal $principal -Settings $settings | Out-Null

    # Best-effort first convergence so hooks are available immediately after
    # installation. If nobody is signed in, hook-install reports no active
    # console user and the logon trigger converges at the next sign-in.
    try { Start-ScheduledTask -TaskName $TaskName } catch { }

    exit 0
}
catch {
    Write-Error $_
    exit 1
}
