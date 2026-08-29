<#
.SYNOPSIS
Compiles dist\obot-sentry.msi from a prebuilt obot-sentry.exe.

.DESCRIPTION
Runs WiX v4 (`dotnet tool install --global wix`) against obot-sentry.wxs. The
installer keeps the fixed name obot-sentry.msi on purpose: Windows Installer
decides upgrades from the metadata inside the package, never the file
name, and a fixed name lets the MDM's stored install command survive
every release untouched.

.PARAMETER Version
Release version, stamped into the MSI as its ProductVersion.

.PARAMETER Exe
The obot-sentry.exe to package. CI cross-compiles it; for a local build:
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o bin\obot-sentry.exe .

.PARAMETER Launcher
The obot-sentryw.exe the scan task runs; must be linked -H=windowsgui, which
this script enforces. For a local build:
$env:GOOS='windows'; $env:GOARCH='amd64'
go build -ldflags="-H=windowsgui" -o bin\obot-sentryw.exe .\cmd\obot-sentryw
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string] $Version,

    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string] $Exe,

    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string] $Launcher
)

$ErrorActionPreference = 'Stop'

# ProductVersion has hard field limits (255.255.65535) and exactly three
# parts. [version] handles the numeric parsing; the shape and bounds are
# ours to enforce. Everything downstream trusts this gate.
function Assert-ProductVersion([string] $Candidate) {
    $parsed = $Candidate -as [version]
    $ok = $null -ne $parsed -and
        $parsed.Build -ge 0 -and $parsed.Revision -lt 0 -and
        $parsed.Major -le 255 -and $parsed.Minor -le 255 -and
        $parsed.Build -le 65535
    if (-not $ok) {
        throw "'$Candidate' cannot be an MSI ProductVersion: expected a.b.c with a,b <= 255 and c <= 65535."
    }
}

# Refuses any image that isn't x64 of the expected subsystem: a launcher built
# without -H=windowsgui is console-subsystem, and visible. PE offset at 0x3C;
# machine 4 bytes on, subsystem at +0x18+0x44.
$IMAGE_SUBSYSTEM_WINDOWS_GUI = 2
$IMAGE_SUBSYSTEM_WINDOWS_CUI = 3
function Assert-WindowsImage {
    param(
        [Parameter(Mandatory)][string] $Path,
        [Parameter(Mandatory)][ValidateSet(2, 3)][int] $Subsystem
    )
    $header = [byte[]]::new(0x40)
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        if ($stream.Read($header, 0, $header.Length) -lt $header.Length) {
            throw "'$Path' is too small to be a Windows executable."
        }
        $signatureOffset = [System.BitConverter]::ToUInt32($header, 0x3C)
        $stream.Position = $signatureOffset
        # Through the optional header's Subsystem field, at 0x18 + 0x44.
        $fields = [byte[]]::new(0x5E)
        if ($stream.Read($fields, 0, $fields.Length) -lt $fields.Length) {
            throw "'$Path' is truncated."
        }
    }
    finally {
        $stream.Dispose()
    }
    if ([System.BitConverter]::ToUInt32($fields, 0) -ne 0x00004550) {
        throw "'$Path' has no PE signature."
    }
    $machine = [System.BitConverter]::ToUInt16($fields, 4)
    if ($machine -ne 0x8664) {
        throw ("'{0}' targets machine type 0x{1:X4}; obot-sentry.msi only ships x64." -f $Path, $machine)
    }
    $actual = [System.BitConverter]::ToUInt16($fields, 0x5C)
    if ($actual -ne $Subsystem) {
        $names = @{ 2 = 'GUI (windowless)'; 3 = 'console' }
        $actualName = $names[[int]$actual]
        if (-not $actualName) { $actualName = "subsystem $actual" }
        throw ("'{0}' is a {1} image; obot-sentry.msi needs a {2} one here. Check the -H=windowsgui link flag." -f
            $Path, $actualName, $names[$Subsystem])
    }
}

Assert-ProductVersion $Version

if (-not (Test-Path -LiteralPath $Exe -PathType Leaf)) {
    throw "No executable at '$Exe'."
}
$binary = (Resolve-Path -LiteralPath $Exe).ProviderPath
Assert-WindowsImage -Path $binary -Subsystem $IMAGE_SUBSYSTEM_WINDOWS_CUI

if (-not (Test-Path -LiteralPath $Launcher -PathType Leaf)) {
    throw "No launcher at '$Launcher'."
}
$launcherBinary = (Resolve-Path -LiteralPath $Launcher).ProviderPath
Assert-WindowsImage -Path $launcherBinary -Subsystem $IMAGE_SUBSYSTEM_WINDOWS_GUI

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).ProviderPath
$outDir = Join-Path $repoRoot 'dist'
$null = New-Item -ItemType Directory -Force -Path $outDir
$installer = Join-Path $outDir 'obot-sentry.msi'

Write-Host "wix: obot-sentry.wxs + $binary -> $installer (ProductVersion $Version)"

# obot-sentry.wxs pulls obot-sentry.ico and both scheduler scripts from the
# bind path (this directory); the two executables arrive through the ExePath
# and LauncherPath preprocessor variables.
# -arch x64 makes ProgramFiles64Folder and component bitness 64-bit.
& wix build `
    -arch x64 `
    -src (Join-Path $PSScriptRoot 'obot-sentry.wxs') `
    -bindpath $PSScriptRoot `
    -d "Version=$Version" `
    -d "ExePath=$binary" `
    -d "LauncherPath=$launcherBinary" `
    -out $installer

if ($LASTEXITCODE -ne 0) {
    throw "wix exited with code $LASTEXITCODE."
}
