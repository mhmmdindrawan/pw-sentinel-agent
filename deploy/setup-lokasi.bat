@echo off
rem Klik dua kali untuk memasang Parkways Sentinel di server lokasi Windows.
rem Folder bawaan D:\ParkwaysSentinel. Folder lain: setup-lokasi.bat -Dir E:\ParkwaysSentinel
setlocal
net session >nul 2>&1
if not errorlevel 1 goto run
echo Meminta izin Administrator...
if "%~1"=="" goto elevate_noargs
powershell -NoProfile -Command "Start-Process -FilePath %~f0 -ArgumentList %* -Verb RunAs"
exit /b
:elevate_noargs
powershell -NoProfile -Command "Start-Process -FilePath %~f0 -Verb RunAs"
exit /b
:run
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0setup-lokasi.ps1" %*
echo.
pause
