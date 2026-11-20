@echo off
rem Klik dua kali untuk memasang Parkways Sentinel di server lokasi Windows.
rem Boleh dijalankan dari folder mana saja (Downloads, Desktop, flashdisk).
rem Sentinel dipasang di D:\ParkwaysSentinel. Folder lain: setup-lokasi.bat -Dir E:\ParkwaysSentinel
setlocal
if exist "%~dp0setup-lokasi.ps1" goto check_admin
echo.
echo File setup-lokasi.ps1 tidak ada di folder ini:
echo   %~dp0
echo.
echo Zip belum diekstrak. Klik kanan file zip, pilih "Extract All",
echo lalu jalankan setup-lokasi.bat dari folder hasil ekstrak.
echo.
pause
exit /b 1

:check_admin
net session >nul 2>&1
if not errorlevel 1 goto run
echo Meminta izin Administrator...
rem Path dan argumen diberi tanda kutip: folder dengan spasi atau tanda
rem kurung (C:\Users\Nama User\Downloads\... (1)) tetap bisa.
if "%~1"=="" goto elevate_noargs
powershell -NoProfile -ExecutionPolicy Bypass -Command "try { Start-Process -FilePath '%~f0' -ArgumentList '%*' -Verb RunAs -ErrorAction Stop } catch { exit 1 }"
goto elevated
:elevate_noargs
powershell -NoProfile -ExecutionPolicy Bypass -Command "try { Start-Process -FilePath '%~f0' -Verb RunAs -ErrorAction Stop } catch { exit 1 }"
:elevated
if not errorlevel 1 exit /b 0
echo.
echo Izin Administrator tidak diberikan atau gagal diminta.
echo Coba: klik kanan setup-lokasi.bat, pilih "Run as administrator".
echo.
pause
exit /b 1

:run
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0setup-lokasi.ps1" %*
echo.
pause
