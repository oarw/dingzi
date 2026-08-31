# Starts a panel and an agent, runs the API and terminal checks, tears down.
#
# Deliberately uses a throwaway data directory each run: the panel generates its
# secrets on first start, so reusing one would skip the path that matters.

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$dataDir  = Join-Path $PSScriptRoot "data"
$agentCfg = Join-Path $PSScriptRoot "agent.yaml"
$srvLog   = Join-Path $PSScriptRoot "server.log"
$agtLog   = Join-Path $PSScriptRoot "agent.log"

Remove-Item -Recurse -Force $dataDir, $agentCfg, $srvLog, $agtLog -ErrorAction SilentlyContinue

$port = 8099
$base = "http://127.0.0.1:$port"

Write-Host "building..."
go build -ldflags "-X main.version=0.2.0-e2e" -o "$PSScriptRoot\dingzi-server.exe" ./cmd/server
go build -ldflags "-X main.version=0.2.0-e2e" -o "$PSScriptRoot\dingzi-agent.exe"  ./cmd/agent

Write-Host "starting panel..."
$srv = Start-Process -FilePath "$PSScriptRoot\dingzi-server.exe" `
  -ArgumentList "--listen","127.0.0.1:$port","--data",$dataDir,"--terminal" `
  -RedirectStandardOutput $srvLog -RedirectStandardError "$srvLog.err" `
  -PassThru -NoNewWindow

try {
  # Wait for the banner to carry the generated credentials rather than sleeping a
  # fixed amount and hoping.
  $secret = $null; $password = $null
  foreach ($i in 1..60) {
    Start-Sleep -Milliseconds 400
    if (-not (Test-Path $srvLog)) { continue }
    $text = Get-Content $srvLog -Raw
    if ($text -match "Agent 密钥:\s*(\S+)")   { $secret = $Matches[1] }
    if ($text -match "管理员密码:\s*(\S+)")   { $password = $Matches[1] }
    if ($secret -and $password) { break }
  }
  if (-not $secret)   { throw "never saw the agent secret in the banner" }
  if (-not $password) { throw "never saw the admin password in the banner" }
  Write-Host "panel up. secret length $($secret.Length), password length $($password.Length)"

  Write-Host "starting agent with --allow-terminal..."
  $agt = Start-Process -FilePath "$PSScriptRoot\dingzi-agent.exe" `
    -ArgumentList "--server",$base,"--secret",$secret,"--config",$agentCfg, `
                  "--name","e2e-box","--allow-terminal" `
    -RedirectStandardOutput $agtLog -RedirectStandardError "$agtLog.err" `
    -PassThru -NoNewWindow

  # Wait until the machine is online with a sample, not merely registered.
  $id = $null
  foreach ($i in 1..60) {
    Start-Sleep -Milliseconds 500
    try {
      $fleet = Invoke-RestMethod "$base/api/v1/servers"
      if ($fleet.servers.Count -ge 1 -and $fleet.servers[0].online -and
          $fleet.servers[0].mem_total -gt 0) {
        $id = $fleet.servers[0].id; break
      }
    } catch { }
  }
  if (-not $id) { throw "agent never came online" }
  Write-Host "agent online as machine $id`n"

  node "$PSScriptRoot\check.mjs" $base $password $id
  $checkExit = $LASTEXITCODE
}
finally {
  Write-Host "`nstopping..."
  if ($agt -and -not $agt.HasExited) { Stop-Process -Id $agt.Id -Force }
  if ($srv -and -not $srv.HasExited) { Stop-Process -Id $srv.Id -Force }
}

Write-Host "`n--- agent log (terminal lines) ---"
Get-Content "$agtLog.err" -ErrorAction SilentlyContinue |
  Select-String -Pattern "terminal|shell" | Select-Object -First 10

Write-Host "`n--- panel log (terminal lines) ---"
Get-Content "$srvLog.err" -ErrorAction SilentlyContinue |
  Select-String -Pattern "terminal" | Select-Object -First 10

exit $checkExit
