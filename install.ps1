#Requires -Version 5.1
$ErrorActionPreference = "Stop"

$Repo = if ($env:APX_REPO) { $env:APX_REPO } else { "bberka/agentpassage" }
$Branch = if ($env:APX_BRANCH) { $env:APX_BRANCH } else { "main" }
$InstallDir = if ($env:APX_INSTALL_DIR) { $env:APX_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "apx\bin" }

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error "Go (1.24+) is required to build AgentPassage (apx) from source. Install via 'winget install GoLang.Go' or https://go.dev/dl/"
    exit 1
}

$Tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("apx-install-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null

try {
    $SrcDir = Join-Path $Tmp "src"
    if (Get-Command git -ErrorAction SilentlyContinue) {
        Write-Host "Cloning AgentPassage ($Repo@$Branch)..."
        git clone --depth 1 --branch $Branch "https://github.com/$Repo.git" $SrcDir | Out-Null
    } else {
        $ZipUrl = "https://github.com/$Repo/archive/refs/heads/$Branch.zip"
        $ZipPath = Join-Path $Tmp "src.zip"
        Write-Host "Downloading AgentPassage source from $ZipUrl..."
        Invoke-WebRequest -Uri $ZipUrl -OutFile $ZipPath -UseBasicParsing
        Expand-Archive -Path $ZipPath -DestinationPath $Tmp -Force
        $Extracted = Get-ChildItem -Path $Tmp -Directory | Where-Object { $_.Name -ne "src" } | Select-Object -First 1
        Move-Item -Path $Extracted.FullName -Destination $SrcDir
    }

    Write-Host "Building apx.exe..."
    $OutExe = Join-Path $Tmp "apx.exe"
    Push-Location $SrcDir
    try {
        $env:CGO_ENABLED = "0"
        & go build -trimpath -ldflags="-s -w" -o $OutExe ./cmd/apx
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $TargetExe = Join-Path $InstallDir "apx.exe"
    $OldExe = Join-Path $InstallDir "apx.exe.old"

    if (Test-Path $OldExe) { Remove-Item $OldExe -Force -ErrorAction SilentlyContinue }
    if (Test-Path $TargetExe) {
        try {
            Move-Item -Path $TargetExe -Destination $OldExe -Force
        } catch {
            # If locked, try stopping service first
            & $TargetExe service stop 2>$null | Out-Null
            Start-Sleep -Milliseconds 500
            Move-Item -Path $TargetExe -Destination $OldExe -Force
        }
    }
    Move-Item -Path $OutExe -Destination $TargetExe -Force
    Remove-Item $OldExe -Force -ErrorAction SilentlyContinue

    # Add InstallDir to user PATH if not already present
    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $PathParts = @()
    if ($UserPath) { $PathParts = $UserPath -split ";" | Where-Object { $_ -ne "" } }
    if ($PathParts -notcontains $InstallDir) {
        $NewPath = ($PathParts + $InstallDir) -join ";"
        [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
        $env:Path = "$InstallDir;$env:Path"
        Write-Host "Added $InstallDir to your User PATH."
    }

    Write-Host "Installed apx to $TargetExe"

    # Restart background service if already installed
    $SvcStatus = (& $TargetExe service status 2>$null) -join "`n"
    if ($SvcStatus -match "Installed:\s*true") {
        Write-Host "Restarting apx background service..."
        & $TargetExe service restart | Out-Null
    }

    Write-Host ""
    Write-Host "Run 'apx' to open the TUI, or 'apx service install --auto-start' to run in the background."
} finally {
    Remove-Item -Path $Tmp -Recurse -Force -ErrorAction SilentlyContinue
}
