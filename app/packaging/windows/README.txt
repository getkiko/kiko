Kiko Server for Windows (portable)

1. Extract the whole folder and double-click start.cmd.
2. Open the Kiko desktop app to connect to the local server (127.0.0.1:9099).
3. Press Ctrl+C to stop the server. Wait for device cleanup and system proxy
   restoration to finish before closing the window.

The package contains kiko-server.exe and kiko-core.exe for the same
architecture. Use the amd64 package on typical Intel/AMD PCs and the arm64
package on Windows on ARM.

Configuration and logs are stored in %APPDATA%\kiko-server and are kept when
the program folder is upgraded or moved. To use a TUN adapter, run start.cmd
as administrator.

Change the data directory from Command Prompt (LISTEN accepts loopback
addresses only):
  set "DATA_DIR=D:\KikoData"
  start.cmd

To allow other devices, add --remote-listen :9443 to enable TLS remote access,
run kiko-server.exe pair to generate a pairing code, and allow port 9443 in
Windows Firewall.
