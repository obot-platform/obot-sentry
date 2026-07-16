<#
.SYNOPSIS
Compiles dist\obocop.msi from a prebuilt obocop.exe.

.DESCRIPTION
Runs WiX v4 (`dotnet tool install --global wix`) against obocop.wxs. The
installer keeps the fixed name obocop.msi on purpose: Windows Installer
decides upgrades from the metadata inside the package, never the file
name, and a fixed name lets the MDM's stored install command survive
every release untouched.

.PARAMETER Version
Release version, stamped into the MSI as its ProductVersion.

.PARAMETER Exe
The obocop.exe to package. CI cross-compiles it; for a local build:
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o bin\obocop.exe .
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string] $Version,

    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string] $Exe
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

# Refuses anything that isn't an x64 Windows image, so a mixed-up
# artifact can never ship inside the installer. IMAGE_FILE_MACHINE_AMD64
# is 0x8664; the machine field sits four bytes past the PE signature,
# whose offset lives at 0x3C in the DOS header.
function Assert-Amd64Image([string] $Path) {
    $header = [byte[]]::new(0x40)
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        if ($stream.Read($header, 0, $header.Length) -lt $header.Length) {
            throw "'$Path' is too small to be a Windows executable."
        }
        $signatureOffset = [System.BitConverter]::ToUInt32($header, 0x3C)
        $stream.Position = $signatureOffset
        $fields = [byte[]]::new(6)
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
        throw ("'{0}' targets machine type 0x{1:X4}; obocop.msi only ships x64." -f $Path, $machine)
    }
}

Assert-ProductVersion $Version

if (-not (Test-Path -LiteralPath $Exe -PathType Leaf)) {
    throw "No executable at '$Exe'."
}
$binary = (Resolve-Path -LiteralPath $Exe).ProviderPath
Assert-Amd64Image $binary

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).ProviderPath
$outDir = Join-Path $repoRoot 'dist'
$null = New-Item -ItemType Directory -Force -Path $outDir
$installer = Join-Path $outDir 'obocop.msi'

Write-Host "wix: obocop.wxs + $binary -> $installer (ProductVersion $Version)"

# obocop.wxs pulls obocop.ico and scan-task.ps1 from the bind path (this
# directory); the exe arrives through the ExePath preprocessor variable.
# -arch x64 makes ProgramFiles64Folder and component bitness 64-bit.
& wix build `
    -arch x64 `
    -src (Join-Path $PSScriptRoot 'obocop.wxs') `
    -bindpath $PSScriptRoot `
    -d "Version=$Version" `
    -d "ExePath=$binary" `
    -out $installer

if ($LASTEXITCODE -ne 0) {
    throw "wix exited with code $LASTEXITCODE."
}
