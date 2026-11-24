# Register / remove the machine-wide 'Obot Sentry Hook Install' scheduled task.
# Run by the MSI's custom actions (SYSTEM). The task runs hook-install with an
# elevated SYSTEM token so it can converge machine policy and the hook files of
# every user profile on the machine, signed in or not.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -NoProfile -File hook-task.ps1 -Mode install -ExePath "C:\Program Files\Obot\obot-sentry\obot-sentry.exe"
#   powershell -ExecutionPolicy Bypass -NoProfile -File hook-task.ps1 -Mode uninstall -ExePath "C:\Program Files\Obot\obot-sentry\obot-sentry.exe"

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('install', 'uninstall')][string]$Mode,
    [string]$ExePath
)

$ErrorActionPreference = 'Stop'
$TaskName = 'Obot Sentry Hook Install'

try {
    if ($Mode -eq 'uninstall') {
        # Stop a run in progress first: unregistering does not, and a running
        # check holds its directory open.
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        # hook-install's run log and periodic-check record, removed by the binary
        # through the same held, junction-safe handles it wrote them with. A
        # path-based removal here could be redirected by a junction planted
        # anywhere above the directory.
        if ($ExePath -and (Test-Path -LiteralPath $ExePath -PathType Leaf)) {
            try { & $ExePath hook-install --remove-run-data 2>&1 | Out-Null } catch { }
        }
        exit 0
    }

    if (-not $ExePath) { throw '-ExePath is required for install' }
    if (-not (Test-Path -LiteralPath $ExePath -PathType Leaf)) {
        throw "obot-sentry.exe not found at $ExePath"
    }

    # --if-due makes each start a cheap check: hook-install converges only when
    # one is due (the HookCheckIntervalMinutes interval, default 15, has
    # passed; the machine restarted; a user profile appeared; or the version or
    # enforcement setting changed) and otherwise exits at once.
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

    # Check at startup, at logon, and every five minutes - the floor the
    # check interval is clamped to, so no configured interval waits on the
    # trigger. hook-install covers every user profile, not only the one
    # signed in, so a startup run with nobody signed in still has work to do;
    # the delays let profile and file-system services settle first. The logon
    # trigger catches a profile created by a first sign-in without waiting for
    # the next tick. StartWhenAvailable also lets Task Scheduler catch up after
    # sleep; IgnoreNew prevents overlapping convergence.
    $startupTrigger = New-ScheduledTaskTrigger -AtStartup
    $startupTrigger.Delay = 'PT1M'
    $logonTrigger = New-ScheduledTaskTrigger -AtLogOn
    $logonTrigger.Delay = 'PT1M'
    $clockTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) `
        -RepetitionInterval (New-TimeSpan -Minutes 5)
    $triggers = @($startupTrigger, $logonTrigger, $clockTrigger)
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit (New-TimeSpan -Minutes 30)

    Register-ScheduledTask -TaskName $TaskName -Force `
        -Description 'Check and repair Obot Sentry local-agent hooks' `
        -Action $action -Trigger $triggers -Principal $principal -Settings $settings | Out-Null

    # Best-effort first convergence so hooks are available immediately after
    # installation. The binary forgets its last check first, through the same
    # junction-safe path it uses for the record, so even a reinstall of the
    # same version to restore hooks converges now.
    # A check already running would record itself after the clear, so stop it
    # first.
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    try { & $ExePath hook-install --clear-check-record 2>&1 | Out-Null } catch { }
    try { Start-ScheduledTask -TaskName $TaskName } catch { }

    exit 0
}
catch {
    Write-Error $_
    exit 1
}
