@echo off
setlocal enabledelayedexpansion
rem Cross-compile every supported target into .\dist, then offer to move this
rem platform's imgc + imgo into %USERPROFILE%\bn. Windows counterpart of setup.sh.

set "dist=%CD%\dist"
if exist "%dist%" rmdir /s /q "%dist%"
mkdir "%dist%"

for /f "delims=" %%i in ('go env GOOS') do set "host_os=%%i"
for /f "delims=" %%i in ('go env GOARCH') do set "host_arch=%%i"
for /f "delims=" %%i in ('powershell -Command "(Get-Date).ToString(''yy.MM.dd'')"') do set "VERSION=%%i"

echo Products will be generated into .\dist (%dist%)
echo.

echo   imgc ...
for %%p in (linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64) do (
    for /f "tokens=1,2 delims=/" %%a in ("%%p") do (
        set "os=%%a"
        set "arch=%%b"
        set "ext="
        if "!os!"=="windows" set "ext=.exe"
        set "CGO_ENABLED=0"
        go build -trimpath -ldflags="-s -w -X main.buildVersion=!VERSION!" -o "!dist!\imgc-!os!_!arch!!ext!" .
        if errorlevel 1 exit /b 1
        echo     imgc-!os!_!arch!!ext!
    )
)

echo   imgo ...
for %%a in (amd64 arm64) do (
    set "CGO_ENABLED=0"
    go build -trimpath -tags gui -ldflags="-s -w -X main.buildVersion=!VERSION! -H=windowsgui" -o "!dist!\imgo-windows_%%a.exe" .
    if errorlevel 1 exit /b 1
    echo     imgo-windows_%%a.exe
)
echo.

set "ans="
set /p "ans=Move this platform (%host_os%/%host_arch%) imgc + imgo to %USERPROFILE%\bn and delete dist? [Y/n] "
if /i "%ans%"=="n" goto :keep

if not exist "%USERPROFILE%\bn" mkdir "%USERPROFILE%\bn"
move /y "!dist!\imgc-!host_os!_!host_arch!.exe" "%USERPROFILE%\bn\imgc.exe" >nul
move /y "!dist!\imgo-!host_os!_!host_arch!.exe" "%USERPROFILE%\bn\imgo.exe" >nul
rmdir /s /q "!dist!"
dir /b "%USERPROFILE%\bn\imgc.exe" "%USERPROFILE%\bn\imgo.exe"
goto :done

:keep
echo Kept %dist%

:done
echo Desktop entry / format association: not applicable on Windows.
echo Done.
endlocal
