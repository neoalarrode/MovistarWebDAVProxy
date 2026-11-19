# Unidad de red «Nube» en Windows con rclone, contra el WebDAV de la Raspberry Pi.
# Ejecutar en PowerShell como administrador:
#   Set-ExecutionPolicy -Scope Process Bypass; .\instalar-nube.ps1
# Pide el usuario y la contraseña del WebDAV (los del .env de la Pi). La contraseña
# se guarda ofuscada en la configuración de rclone del usuario de Windows.

$ErrorActionPreference = "Stop"
$Url    = "http://192.168.1.63:5050/emby"
$Remoto = "nube-pi"
$Letra  = "N:"

Write-Host "1/4 Instalando rclone y WinFsp..." -ForegroundColor Cyan
winget install --id Rclone.Rclone -e --accept-source-agreements --accept-package-agreements --silent | Out-Null
winget install --id WinFsp.WinFsp -e --accept-source-agreements --accept-package-agreements --silent | Out-Null
$env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" + [Environment]::GetEnvironmentVariable("Path", "User")
$rclone = (Get-Command rclone -ErrorAction SilentlyContinue).Source
if (-not $rclone) { $rclone = (Get-ChildItem "$env:LOCALAPPDATA\Microsoft\WinGet\Packages" -Recurse -Filter rclone.exe | Select-Object -First 1).FullName }
if (-not $rclone) { throw "No encuentro rclone.exe tras instalarlo" }

Write-Host "2/4 Datos del WebDAV de la Pi" -ForegroundColor Cyan
$user = Read-Host "Usuario"
$sec  = Read-Host "Contraseña" -AsSecureString
$pass = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
& $rclone config create $Remoto webdav url=$Url vendor=owncloud user=$user pass=$pass --obscure | Out-Null
$pass = $null
& $rclone lsd "${Remoto}:" | Select-Object -First 3
if ($LASTEXITCODE -ne 0) { throw "No se pudo entrar en el WebDAV: revisa usuario, contraseña y que la Pi esté encendida" }

Write-Host "3/4 Lanzador de la unidad $Letra al iniciar sesión" -ForegroundColor Cyan
$cache = "$env:LOCALAPPDATA\rclone-nube"
$cmd = "@echo off`r`n`"$rclone`" mount ${Remoto}: $Letra --vfs-cache-mode writes --cache-dir `"$cache`" --vfs-cache-max-age 1h --vfs-write-back 5s --dir-cache-time 10m --volname Nube --no-console --log-file `"$cache\mount.log`" --log-level NOTICE`r`n"
New-Item -ItemType Directory -Force -Path $cache | Out-Null
$lanzador = "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup\Nube.cmd"
Set-Content -Path $lanzador -Value $cmd -Encoding ASCII

Write-Host "4/4 Montando $Letra" -ForegroundColor Cyan
Start-Process -FilePath $lanzador -WindowStyle Hidden
Start-Sleep -Seconds 5
if (Test-Path "$Letra\") { Write-Host "Listo: la unidad $Letra (Nube) ya está montada y se montará sola al iniciar sesión." -ForegroundColor Green }
else { Write-Host "La unidad no aparece aún; mira $cache\mount.log" -ForegroundColor Yellow }
