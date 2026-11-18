Kiko Server for macOS

1. Put a kiko-core binary (the Kiko core, based on sing-box) for your CPU
   architecture in this folder, named kiko-core.
2. In Terminal, change to this folder and run:
     chmod +x kiko-core kiko-server *.command
3. Double-click start.command, or run ./start.command in Terminal.
4. Open the Kiko desktop app to connect to the local server. To allow other
   devices, enable remote access under Settings > Remote Access in the app,
   then run ./kiko-server pair to generate a pairing code.

Configuration and logs are stored in ~/Library/Application Support/kiko-server
and are kept when the program folder is replaced or moved.

Run in the background after login:
  Double-click install-launchagent.command

Apple silicon (M1 and later): use the arm64 package.
Intel Macs: use the amd64 package.

macOS may block the first launch. Allow it in System Settings > Privacy &
Security, or run in Terminal:
  xattr -dr com.apple.quarantine .

A paired device has full control over the configuration and processes.
Revoke devices you no longer use.
