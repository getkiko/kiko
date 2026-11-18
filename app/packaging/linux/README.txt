Kiko Server for Linux

kiko-server manages kiko-core (the Kiko core, based on sing-box) on a
headless host and is controlled remotely from the Kiko desktop app. The loopback address 127.0.0.1:9099 serves clients
on the same machine; other devices connect over TLS (port 9443 by default)
and must pair on first connect.

Quick start
1. Upload this whole directory to the server.
2. The package includes kiko-core for the same CPU architecture; to use
   another build, put it in this directory named kiko-core.
3. Run:
     chmod +x kiko-core kiko-server start.sh install-service.sh
     ./start.sh
4. In another terminal, generate a pairing code:
     ./start.sh pair
   The output contains a pairing string, an 8-character code and the first
   8 characters of the certificate fingerprint. The code is valid for
   5 minutes and can be used once.
5. In the desktop app, open Settings > Servers > Add server and paste the
   pairing string. Alternatively, enter the server address (for example
   192.168.1.2:9443) and the code, and compare the certificate fingerprint.

Run as a background service (systemd, remote access enabled by default):
  sudo ./install-service.sh
  ./start.sh pair
The service runs as the current user with CAP_NET_ADMIN and related network
capabilities, so TUN works directly. When running ./start.sh by hand, TUN
needs root, or grant the capabilities first:
  sudo setcap cap_net_admin,cap_net_raw,cap_net_bind_service+ep kiko-core

Start kiko-core automatically and keep retrying on failure (up to every
30 seconds):
  ./start.sh --auto-start
  sudo ./install-service.sh  # the installed systemd service enables this flag

Stopping the core by hand cancels the retries for the current run. The next
start with --auto-start starts it again.

Custom port or data directory:
  REMOTE_LISTEN=:19443 DATA_DIR=/var/lib/kiko ./start.sh
  REMOTE_LISTEN= ./start.sh   # keep the previously saved remote access setting
  DATA_DIR=/var/lib/kiko ./start.sh pair
--listen (LISTEN) accepts loopback addresses only; remote devices always
connect through the TLS port.

Firewall
Remote devices need TCP port 9443 (or the port set by REMOTE_LISTEN), e.g.:
  sudo ufw allow 9443/tcp
  sudo firewall-cmd --permanent --add-port=9443/tcp && sudo firewall-cmd --reload
Open the port to trusted networks only.

Devices
Paired devices are listed under Settings > Remote Access in the desktop app,
where they can be revoked; a revoked device is disconnected immediately.
The certificate and device list are kept in the data directory:
remote-cert.pem, remote-key.pem and remote-devices.json. Deleting the
certificate makes every client report a changed certificate and pair again.

Service management:
  sudo systemctl status kiko-server
  sudo systemctl restart kiko-server
  sudo journalctl -u kiko-server -f

Security: a paired device has full control over the configuration and
processes. Only give pairing codes to your own devices, and revoke devices
you no longer use.
