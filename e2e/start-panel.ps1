# Starts the panel fully detached and writes its generated credentials to
# banner.txt, so the caller returns instead of inheriting the console.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$port    = 8099
$dataDir = Join-Path $PSScriptRoot "live-data"
$banner  = Join-Path $PSScriptRoot "banner.txt"

go build -ldflags "-X main.version=0.2.0-live" -o "$PSScriptRoot\dingzi-server.exe" ./cmd/server

$si = New-Object System.Diagnostics.ProcessStartInfo
$si.FileName               = "$PSScriptRoot\dingzi-server.exe"
$si.Arguments              = "--listen 0.0.0.0:$port --data `"$dataDir`" --terminal"
$si.RedirectStandardOutput = $true
$si.RedirectStandardError  = $true
$si.UseShellExecute        = $false
$si.CreateNoWindow         = $true

$proc = [System.Diagnostics.Process]::Start($si)

# The banner is printed before the listener blocks, so a short async read gets
# it without waiting for the process to exit.
$stdout = $proc.StandardOutput
$sb = New-Object System.Text.StringBuilder
$deadline = (Get-Date).AddSeconds(20)
while ((Get-Date) -lt $deadline) {
  Start-Sleep -Milliseconds 200
  while ($stdout.Peek() -ge 0) { [void]$sb.AppendLine($stdout.ReadLine()) }
  if ($sb.ToString() -match "Agent 密钥") { break }
}
$text = $sb.ToString()
$text | Set-Content $banner -Encoding UTF8

# Keep draining in the background, or a full pipe buffer eventually blocks the
# panel's own writes.
Start-Job -ScriptBlock {
  param($p)
  $sp = Get-Process -Id $p -ErrorAction SilentlyContinue
  while ($sp -and -not $sp.HasExited) { Start-Sleep -Seconds 5; $sp.Refresh() }
} -ArgumentList $proc.Id | Out-Null

$secret = $null; $password = $null
if ($text -match "Agent 密钥:\s*(\S+)")  { $secret   = $Matches[1] }
if ($text -match "管理员密码:\s*(\S+)")   { $password = $Matches[1] }

@{ port=$port; pid=$proc.Id; secret=$secret; password=$password } |
  ConvertTo-Json | Set-Content (Join-Path $PSScriptRoot "live.json") -Encoding UTF8

Write-Output "PID=$($proc.Id)"
Write-Output "SECRET=$secret"
Write-Output "PASSWORD=$password"
