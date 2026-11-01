@echo off
setlocal enabledelayedexpansion
rem Build THIS platform's imgc + imgo into .\dist, then offer to move them into
rem %USERPROFILE%\bn. Windows counterpart of setup.sh.

set "dist=%CD%\dist"
if exist "%dist%" rmdir /s /q "%dist%"
mkdir "%dist%"

for /f "delims=" %%i in ('go env GOOS') do set "host_os=%%i"
for /f "delims=" %%i in ('go env GOARCH') do set "host_arch=%%i"
for /f "delims=" %%i in ('powershell -Command "(Get-Date).ToString(''yy.MM.dd'')"') do set "VERSION=%%i"

set "ext="
if "%host_os%"=="windows" set "ext=.exe"

echo Building for %host_os%/%host_arch% into .\dist
echo   imgc ...
set "CGO_ENABLED=0"
go build -trimpath -ldflags="-s -w -X main.buildVersion=%VERSION%" -o "%dist%\imgc-%host_os%_%host_arch%%ext%" .
if errorlevel 1 exit /b 1
echo   imgo ...
set "CGO_ENABLED=0"
go build -trimpath -tags gui -ldflags="-s -w -X main.buildVersion=%VERSION% -X main.appName=imgo -H=windowsgui" -o "%dist%\imgo-%host_os%_%host_arch%%ext%" .
if errorlevel 1 exit /b 1
echo.

set "ans="
set /p "ans=Move this platform (%host_os%/%host_arch%) imgc + imgo to %USERPROFILE%\bn and delete dist? [Y/n] "
if /i "%ans%"=="n" goto :keep

if not exist "%USERPROFILE%\bn" mkdir "%USERPROFILE%\bn"
move /y "%dist%\imgc-%host_os%_%host_arch%%ext%" "%USERPROFILE%\bn\imgc%ext%" >nul
move /y "%dist%\imgo-%host_os%_%host_arch%%ext%" "%USERPROFILE%\bn\imgo%ext%" >nul
rmdir /s /q "%dist%"
dir /b "%USERPROFILE%\bn\imgc%ext%" "%USERPROFILE%\bn\imgo%ext%"
goto :done

:keep
echo Kept %dist%

:done
echo Desktop entry / format association: not applicable on Windows.
echo Done.
endlocal
