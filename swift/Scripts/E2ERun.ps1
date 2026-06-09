$ErrorActionPreference = 'Stop'

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$SwiftDir = Split-Path -Parent $ScriptDir
$PackageDir = Join-Path $SwiftDir 'WendyE2ETests'
$RepoDir = Split-Path -Parent $SwiftDir

function ConvertTo-SafeRunID([string]$Value) {
    $safe = $Value -replace '[^A-Za-z0-9._-]', '-'
    while ($safe.Contains('--')) { $safe = $safe.Replace('--', '-') }
    return $safe.Trim('-')
}

$RunID = $env:WENDY_E2E_RUN_ID
$DefaultRunID = $null
$OutputDir = if ($env:WENDY_E2E_OUTPUT_DIR) { $env:WENDY_E2E_OUTPUT_DIR } else { Join-Path (Join-Path $RepoDir 'Build') 'e2e' }

$i = 0
while ($i -lt $args.Count) {
    switch ($args[$i]) {
        '--run-id' { $RunID = $args[$i + 1]; $i += 2; continue }
        '--default-run-id' { $DefaultRunID = $args[$i + 1]; $i += 2; continue }
        '--output-dir' { $OutputDir = $args[$i + 1]; $i += 2; continue }
        default { $i += 1; continue }
    }
}

if (-not $RunID) { $RunID = $DefaultRunID }
if (-not $RunID) { throw 'ERROR: E2ERun.ps1 requires --default-run-id or WENDY_E2E_RUN_ID.' }
$RunID = ConvertTo-SafeRunID $RunID
$env:WENDY_E2E_RUN_ID = $RunID

& (Join-Path $ScriptDir 'E2ETest.ps1') @args
$status = $LASTEXITCODE

$attemptDir = Join-Path $OutputDir $RunID
$runBase = $RunID -replace '\.[^.]+$', ''
$runName = $runBase -replace '\.[^.]+$', ''
$runDir = Join-Path $OutputDir $runName

Push-Location $PackageDir
try {
    & swift run swift-e2e-testing aggregate --output-dir $OutputDir $attemptDir
    $aggregateStatus = $LASTEXITCODE
} finally {
    Pop-Location
}
if ($status -eq 0 -and $aggregateStatus -ne 0) { $status = $aggregateStatus }

& (Join-Path $ScriptDir 'E2EReview.ps1') --run-dir $runDir
$reviewStatus = $LASTEXITCODE
if ($status -eq 0 -and $reviewStatus -ne 0) { $status = $reviewStatus }

& (Join-Path $ScriptDir 'E2EReport.ps1') --run-dir $runDir
$reportStatus = $LASTEXITCODE
if ($status -eq 0 -and $reportStatus -ne 0) { $status = $reportStatus }

$reportPath = Join-Path $runDir 'index.html'
if (Test-Path -LiteralPath $reportPath) {
    Start-Process $reportPath
} else {
    Write-Output "HTML report not found: $reportPath"
}

exit $status
