# install.ps1 — Install the latest stakecli release on Windows.
#
# Usage (PowerShell):
#   irm https://raw.githubusercontent.com/mnemoo/cli/main/install.ps1 | iex

[CmdletBinding()]
param(
    [string]$Repo    = "mnemoo/cli",
    [string]$Bin     = "stakecli.exe",
    [string]$Dest    = (Join-Path $env:LOCALAPPDATA "stakecli"),
    [string]$Version = $(if ($env:STAKECLI_VERSION) { $env:STAKECLI_VERSION } else { "latest" })
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg" -ForegroundColor Green }
function Write-Warn2($msg){ Write-Host "    $msg" -ForegroundColor Yellow }

# 1. Detect architecture
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    "x86"   { "386" }
    default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

# 2. Resolve release metadata
$api = if ($Version -eq "latest") {
    "https://api.github.com/repos/$Repo/releases/latest"
} else {
    "https://api.github.com/repos/$Repo/releases/tags/$Version"
}

Write-Step "Fetching release info ($Version)"
$release = Invoke-RestMethod -Uri $api -UseBasicParsing
$tag     = $release.tag_name
Write-Ok "version: $tag"

# 3. Pick the matching Windows archive
$asset = $release.assets |
    Where-Object { $_.name -match "(?i)windows" -and $_.name -match "(?i)$arch" -and $_.name -match "\.zip$" } |
    Select-Object -First 1

if (-not $asset) {
    $asset = $release.assets |
        Where-Object { $_.name -match "(?i)windows" -and $_.name -match "\.zip$" } |
        Select-Object -First 1
}
if (-not $asset) { throw "No Windows .zip asset found in release $tag" }

Write-Step "Downloading $($asset.name)"

# 4. Download and extract into a temp workspace
$work = Join-Path $env:TEMP "stakecli_install_$([guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $work -Force | Out-Null
$zip = Join-Path $work $asset.name

try {
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zip -UseBasicParsing
    Expand-Archive -Path $zip -DestinationPath $work -Force

    $exe = Get-ChildItem -Path $work -Filter $Bin -Recurse | Select-Object -First 1
    if (-not $exe) { throw "$Bin not found in archive" }

    # 5. Install
    New-Item -ItemType Directory -Path $Dest -Force | Out-Null
    Copy-Item $exe.FullName -Destination $Dest -Force
    Write-Ok "installed: $(Join-Path $Dest $Bin)"
} finally {
    Remove-Item $work -Recurse -Force -ErrorAction SilentlyContinue
}

# 6. Ensure destination is on user PATH
$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
$parts    = @($userPath -split ";" | Where-Object { $_ })
if ($parts -notcontains $Dest) {
    [Environment]::SetEnvironmentVariable("PATH", (($parts + $Dest) -join ";"), "User")
    Write-Ok "added $Dest to user PATH (restart your terminal to pick it up)"
} else {
    Write-Warn2 "$Dest already on PATH"
}

Write-Host ""
Write-Host "Done. Open a new terminal and run: stakecli" -ForegroundColor Green
