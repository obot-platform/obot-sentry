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
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest

    # Reconcile at boot and hourly. The command targets the active console
    # user, so the boot attempt may legitimately find no user; the hourly
    # trigger (and the best-effort kick below) retries once a user is present.
    # StartWhenAvailable also lets Task Scheduler catch up after sleep; IgnoreNew
    # prevents overlapping convergence.
    $startupTrigger = New-ScheduledTaskTrigger -AtStartup
    $clockTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) `
        -RepetitionInterval (New-TimeSpan -Hours 1)
    $triggers = @($startupTrigger, $clockTrigger)
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit (New-TimeSpan -Minutes 30)

    Register-ScheduledTask -TaskName $TaskName -Force `
        -Description 'Converge Obot Sentry local-agent audit hooks' `
        -Action $action -Trigger $triggers -Principal $principal -Settings $settings | Out-Null

    # Best-effort first convergence so hooks are available immediately after
    # installation. If nobody is signed in, hook-install reports no active
    # console user; the hourly trigger will retry after the next sign-in.
    try { Start-ScheduledTask -TaskName $TaskName } catch { }

    exit 0
}
catch {
    Write-Error $_
    exit 1
}
