# Brings up a panel on Windows with a real Linux agent inside WSL, so the web
# terminal can be exercised against an actual pty.
#
# The panel binds to all interfaces because the WSL guest reaches it over the
# virtual network. That exposes it briefly on the host's network; the password
# and agent secret are generated per run and the panel is stopped at the end.

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$distro   = "dzalpine"
$port     = 8099
$dataDir  = Join-Path $PSScriptRoot "live-data"
$srvLog   = Join-Path $PSScriptRoot "live-server.log"

Remove-Item -Recurse -Force $dataDir, $srvLog, "$srvLog.err" -ErrorAction SilentlyContinue

Write-Host "building panel (windows) and agent (linux)..."
go build -ldflags "-X main.version=0.2.0-live" -o "$PSScriptRoot\dingzi-server.exe" ./cmd/server
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -ldflags "-X main.version=0.2.0-live" -o "$PSScriptRoot\wsl\dingzi-agent" ./cmd/agent
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED

Write-Host "starting panel on 0.0.0.0:$port ..."
$srv = Start-Process -FilePath "$PSScriptRoot\dingzi-server.exe" `
  -ArgumentList "--listen","0.0.0.0:$port","--data",$dataDir,"--terminal" `
  -RedirectStandardOutput $srvLog -RedirectStandardError "$srvLog.err" `
  -PassThru -NoNewWindow

$secret = $null; $password = $null
foreach ($i in 1..60) {
  Start-Sleep -Milliseconds 400
  if (-not (Test-Path $srvLog)) { continue }
  $text = Get-Content $srvLog -Raw
  if ($text -match "Agent 密钥:\s*(\S+)") { $secret = $Matches[1] }
  if ($text -match "管理员密码:\s*(\S+)") { $password = $Matches[1] }
  if ($secret -and $password) { break }
}
if (-not $secret) { throw "no agent secret in the banner" }

# The WSL guest reaches the Windows host on its default gateway.
$hostIP = (wsl -d $distro -- /bin/sh -c "ip route | awk '/^default/ {print \$3}'").Trim()
Write-Host "windows host is $hostIP from inside $distro"

New-NetFirewallRule -DisplayName "dingzi-e2e-$port" -Direction Inbound `
  -LocalPort $port -Protocol TCP -Action Allow -ErrorAction SilentlyContinue | Out-Null

Write-Host "starting linux agent inside $distro with --allow-terminal ..."
$agentCmd = "cp /mnt/c/Users/runneradmin/Desktop/pro/dingzi/e2e/wsl/dingzi-agent /tmp/ && " +
            "chmod +x /tmp/dingzi-agent && " +
            "nohup /tmp/dingzi-agent --server http://${hostIP}:$port --secret $secret " +
            "--config /tmp/agent.yaml --name alpine-container --allow-terminal " +
            "> /tmp/agent.log 2>&1 & echo started"
wsl -d $distro -- /bin/sh -c $agentCmd | Out-Null

$id = $null
foreach ($i in 1..60) {
  Start-Sleep -Milliseconds 500
  try {
    $fleet = Invoke-RestMethod "http://127.0.0.1:$port/api/v1/servers"
    if ($fleet.servers.Count -ge 1 -and $fleet.servers[0].online) {
      $id = $fleet.servers[0].id; break
    }
  } catch { }
}
if (-not $id) {
  Write-Host "--- agent log ---"
  wsl -d $distro -- /bin/sh -c "cat /tmp/agent.log" 2>&1 | Select-Object -First 20
  throw "linux agent never came online"
}

$m = (Invoke-RestMethod "http://127.0.0.1:$port/api/v1/servers").servers[0]
Write-Host ""
Write-Host "READY"
Write-Host "  url:      http://127.0.0.1:$port"
Write-Host "  password: $password"
Write-Host "  machine:  $id ($($m.name)) $($m.platform) $($m.arch)"
Write-Host "  terminal: agent=$($m.terminal_enabled)"
Write-Host "  pid:      $($srv.Id)"
Write-Host ""
Write-Host "stop with: Stop-Process -Id $($srv.Id)"

# Written so the screenshot step can pick them up without re-parsing the banner.
@{url="http://127.0.0.1:$port"; password=$password; id=$id; pid=$srv.Id} |
  ConvertTo-Json | Set-Content (Join-Path $PSScriptRoot "live.json")
