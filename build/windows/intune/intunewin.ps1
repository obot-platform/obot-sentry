<#
.SYNOPSIS
Packages dist\obocop.msi as dist\obocop.intunewin for Intune.

.DESCRIPTION
Drives Microsoft's Win32 Content Prep Tool, which must be on PATH as
IntuneWinAppUtil.exe (CI fetches a pinned copy in build.yaml; grab it
from https://github.com/microsoft/Microsoft-Win32-Content-Prep-Tool for
local runs). The tool embeds the MSI's product code and version into the
package metadata, which is what lets Intune prefill the detection rule
and drive supersedence — the file name itself carries no meaning.

Run msi.ps1 first; this script only repackages its output.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).ProviderPath
$outDir = Join-Path $repoRoot 'dist'
$installer = Join-Path $outDir 'obocop.msi'

if (-not (Test-Path -LiteralPath $installer -PathType Leaf)) {
    throw "Expected $installer to exist — run msi.ps1 before this script."
}

Write-Host "content-prep: $installer -> obocop.intunewin"

& IntuneWinAppUtil.exe -c $outDir -s 'obocop.msi' -o $outDir -q
if ($LASTEXITCODE -ne 0) {
    throw "IntuneWinAppUtil.exe exited with code $LASTEXITCODE."
}

# The tool derives its output name from the setup file, so this is where
# obocop.intunewin must have appeared.
$package = [System.IO.Path]::ChangeExtension($installer, '.intunewin')
if (-not (Test-Path -LiteralPath $package -PathType Leaf)) {
    throw "Content prep finished without writing $package."
}
