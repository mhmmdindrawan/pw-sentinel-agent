<#
Setup cepat Parkways Sentinel di server lokasi WINDOWS.

Cara pakai: taruh file ini satu folder dengan
  sentinel-agent-<versi>-windows-amd64.exe, registry.yaml, config.example.yaml,
  setup-lokasi.bat
lalu klik dua kali setup-lokasi.bat (minta izin Administrator) dan ketik
nama lokasi. Folder pemasangan bawaan: D:\ParkwaysSentinel.

Dari PowerShell (Administrator):
  powershell -ExecutionPolicy Bypass -File setup-lokasi.ps1 [-Name "NAMA"] [-Code KODE]
      [-Dir E:\ParkwaysSentinel] [-Port 8787] [-Binary file.exe] [-NoService]

Yang dilakukan:
  1. Ambil IP Tailscale PC ini dan pastikan IP itu terpasang di PC ini.
  2. Cek drive: harus disk internal (Fixed) berformat NTFS, bukan USB,
     bukan folder jaringan.
  3. Salin binary, registry.yaml, config.yaml ke folder pemasangan.
  4. Isi config.yaml: kode, nama, listen_addr, public_url, session_secret.
  5. Buat site.key (kunci lokasi) dan akun admin & teknisi kalau belum ada.
  6. Buka firewall port 8787 hanya untuk jaringan Tailscale (100.64.0.0/10).
  7. Pasang task terjadwal "ParkwaysSentinel" (akun SYSTEM, saat PC menyala)
     yang menjalankan agent, menyalakannya lagi kalau mati, dan pindah ke IP
     baru kalau IP Tailscale berubah (paling lama 1 menit).

Aman dijalankan ulang: kunci, akun, dan data yang sudah ada tidak ditimpa.
File ini sengaja hanya berisi karakter ASCII (Windows PowerShell 5.1
membaca file tanpa BOM sebagai ANSI).
#>
param(
  [string]$Dir = "D:\ParkwaysSentinel",
  [string]$Name = "",
  [string]$Code = "",
  [int]$Port = 0,
  [string]$Binary = "",
  [switch]$NoService,
  [switch]$Run        # mode internal: dijalankan task terjadwal
)

$ErrorActionPreference = "Stop"
$TaskName = "ParkwaysSentinel"
$FwName = "Parkways Sentinel"
$SelfDir = $PSScriptRoot
$Utf8 = New-Object System.Text.UTF8Encoding $false
$OutputEncoding = $Utf8   # password yang di-pipe ke sentinel-agent.exe

function Die([string]$m) { Write-Host "GAGAL: $m" -ForegroundColor Red; exit 1 }
function Info([string]$m) { Write-Host "==> $m" }

# Jalankan program luar tanpa membuat PowerShell 5.1 berhenti karena stderr.
function Invoke-Native([string]$Exe, [string[]]$ArgList) {
  $old = $ErrorActionPreference; $ErrorActionPreference = "Continue"
  try { $out = & $Exe @ArgList 2>$null } finally { $ErrorActionPreference = $old }
  return $out
}

# --- IP Tailscale -----------------------------------------------------------

function Find-Tailscale {
  $c = Get-Command tailscale.exe -ErrorAction SilentlyContinue
  if ($c) { return $c.Source }
  foreach ($p in @("$env:ProgramFiles\Tailscale\tailscale.exe", "${env:ProgramFiles(x86)}\Tailscale\tailscale.exe")) {
    if ($p -and (Test-Path $p)) { return $p }
  }
  return $null
}

function Test-Cgnat([string]$ip) {   # 100.64.0.0/10 = 100.64.x.x - 100.127.x.x
  $p = $ip.Split(".")
  if ($p.Count -ne 4) { return $false }
  $b = 0
  if (-not [int]::TryParse($p[1], [ref]$b)) { return $false }
  return ($p[0] -eq "100" -and $b -ge 64 -and $b -le 127)
}

function Test-OnThisPc([string]$ip) {
  return [bool](Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $ip })
}

# Quiet: kembalikan $null (bukan berhenti) kalau Tailscale belum siap.
function Get-TailscaleIp([int]$Tries = 1, [switch]$Quiet) {
  $ts = Find-Tailscale
  if (-not $ts) {
    if ($Quiet) { return $null }
    Die "Tailscale belum terpasang. Pasang Tailscale dan login dulu."
  }
  $ip = ""
  for ($i = 1; $i -le $Tries; $i++) {
    $ip = "$(Invoke-Native $ts @('ip', '-4') | Select-Object -First 1)".Trim()
    if ($ip -and (Test-Cgnat $ip) -and (Test-OnThisPc $ip)) { return $ip }
    if ($i -lt $Tries) { Start-Sleep -Seconds 2 }
  }
  if ($Quiet) { return $null }
  if (-not $ip) { Die "Tailscale belum login / belum terhubung (cek: tailscale status)." }
  if (-not (Test-Cgnat $ip)) { Die "IP dari tailscale ($ip) bukan IP Tailscale 100.64.0.0/10." }
  Die "IP Tailscale $ip tidak terpasang di PC ini (cek: ipconfig)."
}

# --- config.yaml (kunci di-indent 2 spasi, unik) ----------------------------

function Get-Cfg([string]$key) {
  $t = [IO.File]::ReadAllText($script:CfgPath)
  $m = [regex]::Match($t, "(?m)^  ${key}: *`"?([^`"#\r\n]*)`"?")
  if ($m.Success) { return $m.Groups[1].Value.Trim() }
  return ""
}

function Set-Cfg([string]$key, [string]$val) {
  $t = [IO.File]::ReadAllText($script:CfgPath)
  $rx = New-Object System.Text.RegularExpressions.Regex ("(?m)^  " + [regex]::Escape($key) + ":[^\r\n]*")
  if (-not $rx.IsMatch($t)) { Die "kunci '$key' tidak ada di $($script:CfgPath)" }
  $v = $val.Replace("\", "\\").Replace("`"", "\`"")
  $line = "  ${key}: `"$v`""
  $t = $rx.Replace($t, [System.Text.RegularExpressions.MatchEvaluator] { param($m) $line }, 1)
  [IO.File]::WriteAllText($script:CfgPath, $t, $Utf8)
}

function Sync-Ip([string]$ip, [int]$port) {
  if (-not $port) {
    $port = 8787
    $cur = Get-Cfg "listen_addr"
    if ($cur -match ":(\d+)$") { $port = [int]$Matches[1] }
  }
  $wantListen = "${ip}:$port"
  $wantUrl = "http://${ip}:$port"
  if ((Get-Cfg "listen_addr") -ne $wantListen -or (Get-Cfg "public_url") -ne $wantUrl) {
    Set-Cfg "listen_addr" $wantListen
    Set-Cfg "public_url" $wantUrl
    return $true
  }
  return $false
}

function New-Hex([int]$bytes) {
  $b = New-Object byte[] $bytes
  $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
  $rng.GetBytes($b); $rng.Dispose()
  return (($b | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Stop-Agent {
  Get-Process -Name sentinel-agent -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq $script:AgentExe } |
    Stop-Process -Force -ErrorAction SilentlyContinue
}

# ============================================================================
# Mode -Run: dijalankan task terjadwal (akun SYSTEM) setiap PC menyala.
# Menunggu Tailscale, menyamakan IP, menjalankan agent, mengawasinya.
# ============================================================================

if ($Run) {
  $script:CfgPath = Join-Path $Dir "config.yaml"
  $script:AgentExe = Join-Path $Dir "sentinel-agent.exe"
  Set-Location $Dir
  $logDir = Join-Path $Dir "logs"
  New-Item -ItemType Directory -Force -Path $logDir | Out-Null
  $runLog = Join-Path $logDir "runner.log"
  function Log([string]$m) {
    try {
      if ((Test-Path $runLog) -and (Get-Item $runLog).Length -gt 1MB) { Move-Item -Force $runLog "$runLog.1" }
      Add-Content -Path $runLog -Value ("{0} {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $m)
    } catch { }
  }
  Stop-Agent   # sisa agent dari runner sebelumnya memegang port
  while ($true) {
    try {
      $ip = Get-TailscaleIp -Tries 30 -Quiet          # tunggu sampai +-60 detik
      if (-not $ip) { Log "Tailscale belum siap; coba lagi 30 detik"; Start-Sleep -Seconds 30; continue }
      if (Sync-Ip $ip 0) { Log "IP Tailscale disamakan: $ip" }
      $env:SENTINEL_SITE_KEY = "$(Get-Content -Path (Join-Path $Dir 'site.key') -TotalCount 1)".Trim()
      $errLog = Join-Path $logDir "sentinel.log"
      if (Test-Path $errLog) { Move-Item -Force $errLog (Join-Path $logDir "sentinel.prev.log") }
      $p = Start-Process -FilePath $script:AgentExe -ArgumentList @("-config", "config.yaml") -WorkingDirectory $Dir `
             -NoNewWindow -PassThru -RedirectStandardError $errLog -RedirectStandardOutput (Join-Path $logDir "sentinel.out.log")
      $null = $p.Handle   # supaya ExitCode terbaca setelah proses selesai
      Log "agent jalan (pid $($p.Id)) di ${ip}"
      while (-not $p.WaitForExit(60000)) {
        $now = Get-TailscaleIp -Quiet
        if ($now -and $now -ne $ip) {
          Log "IP Tailscale berubah: $ip -> $now. Agent dinyalakan ulang."
          Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
          break
        }
      }
      $p.WaitForExit(5000) | Out-Null
      Log "agent berhenti (kode $($p.ExitCode)); jalan lagi 5 detik lagi. Lihat logs\sentinel.log"
    } catch {
      Log "runner error: $($_.Exception.Message)"
    }
    Start-Sleep -Seconds 5
  }
}

# ============================================================================
# Pemasangan
# ============================================================================

function Test-Admin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  return (New-Object Security.Principal.WindowsPrincipal $id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# Pesan masalah drive; "" kalau drive boleh dipakai.
function Get-DriveProblem([string]$path) {
  if ($path -like "\\*") { return "folder jaringan (\\...) tidak boleh dipakai: database bisa rusak" }
  if ($path -notmatch "^[A-Za-z]:\\") { return "pakai path lengkap dengan huruf drive, contoh D:\ParkwaysSentinel" }
  $q = $path.Substring(0, 2).ToUpper()
  if (-not (Test-Path "$q\")) { return "drive $q tidak ada di PC ini" }
  $d = New-Object System.IO.DriveInfo $q
  if ($d.DriveType -eq [System.IO.DriveType]::Network) { return "drive $q adalah drive jaringan" }
  if ($d.DriveType -ne [System.IO.DriveType]::Fixed) { return "drive $q bukan disk internal ($($d.DriveType)); saat boot bisa belum terbaca" }
  if ($d.DriveFormat -ne "NTFS") { return "drive $q berformat $($d.DriveFormat), harus NTFS" }
  return ""
}

function Get-SuggestCode([string]$n) {
  $w = @([regex]::Split($n.ToUpper(), "[^A-Z0-9]+") | Where-Object { $_ })
  $c = ""
  if ($w.Count -ge 2) { $c = -join ($w | ForEach-Object { $_.Substring(0, 1) }) }
  elseif ($w.Count -eq 1) { $c = $w[0].Substring(0, [Math]::Min(3, $w[0].Length)) }
  if ($c.Length -gt 10) { $c = $c.Substring(0, 10) }
  return $c
}

function Find-Newest([string[]]$dirs, [string]$filter) {
  foreach ($d in $dirs) {
    if (-not $d -or -not (Test-Path $d)) { continue }
    $f = @(Get-ChildItem -Path $d -Filter $filter -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)
    $stable = @($f | Where-Object { $_.Name -notmatch "-rc[0-9]" })
    if ($stable.Count) { return $stable[0].FullName }
    if ($f.Count) { return $f[0].FullName }
  }
  return $null
}

function Find-First([string[]]$paths) {
  foreach ($p in $paths) { if ($p -and (Test-Path $p)) { return $p } }
  return $null
}

try {
  Write-Host "Parkways Sentinel - pemasangan server lokasi (Windows)"
  Write-Host ""
  if (-not (Test-Admin)) { Die "jalankan sebagai Administrator (klik dua kali setup-lokasi.bat)." }
  if ($env:PROCESSOR_ARCHITECTURE -ne "AMD64" -and $env:PROCESSOR_ARCHITEW6432 -ne "AMD64") {
    Write-Host "Perhatian: PC ini $env:PROCESSOR_ARCHITECTURE; binary Windows hanya untuk x64." -ForegroundColor Yellow
  }

  $ts = Find-Tailscale
  $TsIp = Get-TailscaleIp 1
  Info "Tailscale OK, IP PC ini: $TsIp"

  # Sudah pernah dipasang tanpa -Dir: pakai folder pemasangan lama.
  $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
  if (-not $PSBoundParameters.ContainsKey("Dir") -and $task -and $task.Actions[0].WorkingDirectory) {
    $Dir = $task.Actions[0].WorkingDirectory
    Info "Sudah pernah dipasang di $Dir; folder itu yang dipakai"
  }
  $Dir = $Dir.TrimEnd("\")
  $problem = Get-DriveProblem $Dir
  while ($problem) {
    Write-Host "Folder $Dir tidak bisa dipakai: $problem." -ForegroundColor Yellow
    $Dir = (Read-Host "Ketik folder lain (contoh E:\ParkwaysSentinel)").Trim().TrimEnd("\")
    if (-not $Dir) { Die "tidak ada folder pemasangan" }
    $problem = Get-DriveProblem $Dir
  }
  New-Item -ItemType Directory -Force -Path $Dir | Out-Null
  Set-Location $Dir
  Info "Folder: $Dir"
  $script:CfgPath = Join-Path $Dir "config.yaml"
  $script:AgentExe = Join-Path $Dir "sentinel-agent.exe"

  # --- File ---
  $repoDir = Split-Path -Parent $SelfDir
  if (-not $repoDir) { $repoDir = $SelfDir }   # skrip di akar drive, mis. E:\
  if ($Binary) { $Binary = (Resolve-Path $Binary).Path }
  else { $Binary = Find-Newest @($SelfDir, (Join-Path $repoDir "dist")) "sentinel-agent-*-windows-amd64.exe" }
  if ($Binary) {
    $same = (Test-Path $script:AgentExe) -and ((Get-FileHash $Binary).Hash -eq (Get-FileHash $script:AgentExe).Hash)
    if (-not $same) {
      $upgraded = Test-Path $script:AgentExe
      # Windows mengunci .exe yang sedang jalan: hentikan dulu.
      if ($task) { Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue }
      Stop-Agent
      Start-Sleep -Seconds 1
      if ($upgraded) { Copy-Item -Force $script:AgentExe "$($script:AgentExe).old" }
      Copy-Item -Force $Binary $script:AgentExe
      Unblock-File -Path $script:AgentExe -ErrorAction SilentlyContinue   # tanda "dari internet"
      $ver = "$(Invoke-Native $script:AgentExe @('version'))".Trim()
      if ($upgraded) { Info "Binary: $ver (diperbarui; versi lama: sentinel-agent.exe.old)" } else { Info "Binary: $ver" }
    }
  }
  if (-not (Test-Path $script:AgentExe)) {
    Die "binary tidak ditemukan. Taruh sentinel-agent-<versi>-windows-amd64.exe di folder yang sama dengan skrip ini ($SelfDir)"
  }

  $newReg = Find-First @((Join-Path $SelfDir "registry.yaml"), (Join-Path $repoDir "agent\registry.yaml"))
  $reg = Join-Path $Dir "registry.yaml"
  if ($newReg -and (Test-Path $reg) -and ((Get-FileHash $newReg).Hash -ne (Get-FileHash $reg).Hash)) {
    Copy-Item -Force $reg "$reg.bak"; Copy-Item -Force $newReg $reg
    Info "registry.yaml diperbarui (yang lama: registry.yaml.bak)"
  }
  if (-not (Test-Path $reg)) {
    if (-not $newReg) { Die "registry.yaml tidak ditemukan. Salin ke $SelfDir" }
    Copy-Item $newReg $reg; Info "registry.yaml disiapkan"
  }
  if (-not (Test-Path $script:CfgPath)) {
    $ex = Find-First @((Join-Path $SelfDir "config.example.yaml"), (Join-Path $repoDir "agent\config.example.yaml"))
    if (-not $ex) { Die "config.example.yaml tidak ditemukan. Salin ke $SelfDir" }
    Copy-Item $ex $script:CfgPath; Info "config.yaml disiapkan"
  }

  # --- Identitas lokasi ---
  $siteKey = Join-Path $Dir "site.key"
  $curCode = ""; $curName = ""
  if (Test-Path $siteKey) { $curCode = Get-Cfg "code"; $curName = Get-Cfg "name" }
  while (-not $Name) {
    $prompt = "Nama lokasi"; if ($curName) { $prompt += " [$curName]" }
    $Name = (Read-Host $prompt).Trim()
    if (-not $Name) { $Name = $curName }
  }
  while ($true) {
    if (-not $Code) {
      $sug = $curCode; if (-not $sug) { $sug = Get-SuggestCode $Name }
      $Code = (Read-Host "Kode lokasi, 2-10 huruf/angka [$sug]").Trim()
      if (-not $Code) { $Code = $sug }
    }
    $Code = $Code.ToUpper()
    if ($Code -cmatch "^[A-Z0-9]{2,10}$") { break }
    Write-Host "Kode harus 2-10 huruf/angka tanpa spasi, contoh PCC."; $Code = ""
  }
  if ($curCode -and $Code -ne $curCode) {
    Write-Host "Perhatian: kode lokasi berubah dari $curCode ke $Code. Kode error baru akan memakai $Code." -ForegroundColor Yellow
  }
  Info "Lokasi: $Code - $Name"
  Set-Cfg "code" $Code
  Set-Cfg "name" $Name
  Set-Cfg "enroll_key" ""    # kunci diberikan lewat site.key, bukan ditulis di config
  if (-not $Port) {
    $Port = 8787
    if ((Get-Cfg "listen_addr") -match ":(\d+)$") { $Port = [int]$Matches[1] }
  }
  if (Sync-Ip $TsIp $Port) { Info "IP Tailscale disamakan: ${TsIp}:$Port" }
  if (-not (Get-Cfg "session_secret")) {
    Set-Cfg "session_secret" (New-Hex 32)
    Info "session_secret dibuat (login dashboard tidak hilang saat restart)"
  }

  # --- Kunci lokasi ---
  if (-not (Test-Path $siteKey) -or -not "$(Get-Content $siteKey -TotalCount 1)".Trim()) {
    [IO.File]::WriteAllText($siteKey, (New-Hex 16) + "`n", $Utf8)
    Info "site.key dibuat"
  }

  # --- Akun dashboard: admin / Admin@1234, teknisi / teknisi123 (viewer) ---
  # Bisa diganti lewat env SENTINEL_ADMIN_PASSWORD / SENTINEL_TEKNISI_PASSWORD.
  function Test-User([string]$n) {
    foreach ($l in @(Invoke-Native $script:AgentExe @("user", "list", "-users", "users.yaml"))) {
      if ((("$l".Trim()) -split "\s+")[0] -eq $n) { return $true }
    }
    return $false
  }
  function Add-StdUser([string]$n, [string]$role, [string]$envName, [string]$default) {
    if (Test-User $n) { Info "Akun $n sudah ada, tidak diubah"; return }
    $pw = [Environment]::GetEnvironmentVariable($envName)
    if (-not $pw) { $pw = $default }
    if ($pw.Length -lt 10) { Die "password akun $n kurang dari 10 karakter" }
    $old = $ErrorActionPreference; $ErrorActionPreference = "Continue"
    try { $pw | & $script:AgentExe user add -users users.yaml -name $n -role $role *> $null; $rc = $LASTEXITCODE }
    finally { $ErrorActionPreference = $old }
    if ($rc -ne 0) { Die "gagal membuat akun $n" }
    Info "Akun $n ($role) dibuat"
  }
  Add-StdUser "admin" "admin" "SENTINEL_ADMIN_PASSWORD" "Admin@1234"
  Add-StdUser "teknisi" "viewer" "SENTINEL_TEKNISI_PASSWORD" "teknisi123"

  # Folder hanya untuk SYSTEM dan Administrators (kunci, akun, database).
  # Pakai SID supaya jalan di Windows bahasa apa pun.
  & icacls $Dir /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" /T /Q | Out-Null
  if ($LASTEXITCODE -ne 0) { Write-Host "catatan: izin folder tidak bisa dibatasi (icacls)" -ForegroundColor Yellow }

  $runner = Join-Path $Dir "sentinel-run.ps1"
  Copy-Item -Force $PSCommandPath $runner

  if ($NoService) {
    Write-Host ""
    Write-Host "Selesai tanpa service (-NoService). Jalankan manual:"
    Write-Host "  powershell -ExecutionPolicy Bypass -File `"$runner`" -Run -Dir `"$Dir`""
    exit 0
  }

  # --- Firewall: port hanya untuk jaringan Tailscale ---
  Get-NetFirewallRule -DisplayName $FwName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  New-NetFirewallRule -DisplayName $FwName -Direction Inbound -Protocol TCP -LocalPort $Port `
    -RemoteAddress "100.64.0.0/10" -Action Allow -Profile Any | Out-Null
  Info "Firewall: port $Port dibuka hanya untuk Tailscale"

  # --- Server tidak boleh tidur ---
  & powercfg /change standby-timeout-ac 0 | Out-Null
  & powercfg /change hibernate-timeout-ac 0 | Out-Null
  Info "Sleep/hibernate saat pakai listrik dimatikan"

  # --- Task terjadwal ---
  if ($task) { Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue }
  Stop-Agent
  $act = New-ScheduledTaskAction -Execute "powershell.exe" -WorkingDirectory $Dir `
    -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$runner`" -Run -Dir `"$Dir`""
  $trg = New-ScheduledTaskTrigger -AtStartup
  $set = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit (New-TimeSpan) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable -MultipleInstances IgnoreNew
  $pri = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
  Register-ScheduledTask -TaskName $TaskName -Action $act -Trigger $trg -Settings $set -Principal $pri `
    -Description "Parkways Sentinel ($Code)" -Force | Out-Null
  Start-ScheduledTask -TaskName $TaskName
  Info "Task terjadwal $TaskName dipasang (jalan saat PC menyala, akun SYSTEM)"

  # --- Cek hasil ---
  Info "Menunggu agent menyala..."
  $ok = $false
  for ($i = 0; $i -lt 20; $i++) {
    try {
      $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -Uri "http://${TsIp}:$Port/v1/health"
      if ($r.StatusCode -eq 200) { $ok = $true; break }
    } catch { }
    Start-Sleep -Seconds 1
  }
  Write-Host ""
  if ($ok) { Write-Host "BERHASIL. Sentinel $Code ($Name) berjalan." -ForegroundColor Green }
  else { Write-Host "Agent belum menjawab. Cek log: $Dir\logs\sentinel.log dan $Dir\logs\runner.log" -ForegroundColor Yellow }

  # Nama MagicDNS tidak ikut berubah saat IP Tailscale berubah.
  $nameUrl = ""
  try {
    $dns = ("$((Invoke-Native $ts @('status', '--json')) -join "`n" | ConvertFrom-Json | ForEach-Object { $_.Self.DNSName })").TrimEnd(".")
    if ($dns -and (@([System.Net.Dns]::GetHostAddresses($dns) | ForEach-Object { $_.IPAddressToString }) -contains $TsIp)) {
      $nameUrl = "`n                atau http://${dns}:$Port`n                (tetap sama walau IP Tailscale berubah; pakai kalau bisa dibuka dari kiosk)"
    }
  } catch { }
  $key = "$(Get-Content $siteKey -TotalCount 1)".Trim()
  Write-Host @"

  Dashboard   : http://${TsIp}:$Port
  Login       : admin / Admin@1234   -   teknisi / teknisi123 (hanya melihat)
  Agent URL   : http://${TsIp}:$Port   (diisi di Pengaturan Sentinel setiap gate)$nameUrl
  Kunci lokasi: $key
                (diisi di setiap gate; jangan kirim lewat grup chat.
                 Lihat lagi kapan saja: type $siteKey)
  Log         : $Dir\logs\sentinel.log
"@
  exit 0
} catch {
  Write-Host "GAGAL: $($_.Exception.Message) (baris $($_.InvocationInfo.ScriptLineNumber)). Kirim layar ini ke developer." -ForegroundColor Red
  exit 1
}
