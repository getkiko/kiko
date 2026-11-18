@echo off
setlocal
set "PACKAGE_DIR=%~dp0"
set "PATH=%PACKAGE_DIR%;%PATH%"

if not exist "%PACKAGE_DIR%kiko-server.exe" (
  echo Missing kiko-server.exe in %PACKAGE_DIR%
  pause
  exit /b 1
)
if not exist "%PACKAGE_DIR%kiko-core.exe" (
  echo Missing kiko-core.exe in %PACKAGE_DIR%
  pause
  exit /b 1
)

if not defined LISTEN set "LISTEN=127.0.0.1:9099"
if not defined DATA_DIR (
  set "DATA_DIR=%APPDATA%\kiko-server"
  rem Move the data directory used before the rename to Kiko. If the move fails,
  rem for example while a file in it is open, keep using the old directory
  rem instead of starting with an empty one; the move is retried next time.
  if not exist "%APPDATA%\kiko-server" if exist "%APPDATA%\sbcfg-server\" (
    move "%APPDATA%\sbcfg-server" "%APPDATA%\kiko-server" >nul
    if errorlevel 1 (
      echo Cannot move the old data directory sbcfg-server to kiko-server, keeping the old one.
      set "DATA_DIR=%APPDATA%\sbcfg-server"
    )
  )
)
if not exist "%DATA_DIR%" (
  mkdir "%DATA_DIR%"
  if errorlevel 1 (
    echo Cannot create data directory: %DATA_DIR%
    pause
    exit /b 1
  )
)

echo Kiko: 127.0.0.1:9099
echo Press Ctrl+C to stop the server.
"%PACKAGE_DIR%kiko-server.exe" --core "%PACKAGE_DIR%kiko-core.exe" --listen "%LISTEN%" --data-dir "%DATA_DIR%" %*
if errorlevel 1 pause
