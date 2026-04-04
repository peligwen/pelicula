# Synology NAS Media Stack with ProtonVPN

## Overview

Automated media downloading on your Synology NAS using Container Manager (Docker). Search for shows/movies by name, download via torrent, all traffic routed through ProtonVPN with Wireguard and automatic port forwarding.

**Components:**

- **Gluetun** — VPN container (ProtonVPN + Wireguard + port forwarding)
- **qBittorrent** — torrent client (locked to VPN network)
- **Prowlarr** — indexer manager (torrent search sources)
- **Sonarr** — TV show search & management
- **Radarr** — movie search & management

---

## Part 1: ProtonVPN Setup

You need a **paid** ProtonVPN plan (Plus or higher) — the free tier doesn't support P2P or port forwarding.

### Generate your Wireguard private key

1. Log in at **https://account.proton.me**
2. Go to **Downloads** (left sidebar) → **WireGuard configuration**
3. Configure:
   - **Platform**: GNU/Linux
   - **VPN Options**: select **NAT-PMP (Port Forwarding)**
   - **Important**: do NOT enable "Moderate NAT" — it's incompatible with port forwarding
   - **Server**: pick a P2P-friendly server (e.g., Netherlands, Switzerland, Sweden)
4. Click **Create**
5. **Copy the `PrivateKey` value** from the generated config — this is the only thing you need from it. It looks like: `wOEI9rqqbDwnN8/Bpp22sVz48T71vJ4fYmFWujulwUU=`

> **Note:** The private key works for all ProtonVPN servers, not just the one you selected during generation. Gluetun handles server selection separately.

---

## Part 2: Synology Preparation

### Install Container Manager

1. **Package Center** → search **Container Manager** → Install

### Create folder structure

In **File Station**, create:

```
/volume1/docker/media-stack/
/volume1/docker/media-stack/config/
/volume1/docker/media-stack/config/gluetun/
/volume1/docker/media-stack/config/qbittorrent/
/volume1/docker/media-stack/config/prowlarr/
/volume1/docker/media-stack/config/sonarr/
/volume1/docker/media-stack/config/radarr/

/volume1/media/
/volume1/media/downloads/
/volume1/media/movies/
/volume1/media/tv/
```

### Get your user/group IDs

Enable SSH: **Control Panel → Terminal & SNMP → Enable SSH**

```bash
ssh your_admin_user@YOUR_SYNOLOGY_IP
id
```

Note the `uid` and `gid` (typically `1026` and `100` on Synology).

### Ensure TUN device exists

```bash
ls -la /dev/net/tun
```

If it doesn't exist, create it and make it persistent:

```bash
sudo mkdir -p /dev/net
sudo mknod /dev/net/tun c 10 200
sudo chmod 600 /dev/net/tun
```

Then in DSM: **Control Panel → Task Scheduler → Create → Triggered Task → User-defined script**
- Event: Boot-up
- User: root
- Script:
  ```bash
  mkdir -p /dev/net
  mknod /dev/net/tun c 10 200
  chmod 600 /dev/net/tun
  ```

---

## Part 3: Create the Stack

### Environment file

Create `/volume1/docker/media-stack/.env` (via SSH or File Station text editor):

```env
# ── User IDs (from 'id' command) ─────────────────
PUID=1026
PGID=100
TZ=America/New_York

# ── ProtonVPN Wireguard ──────────────────────────
# Paste your private key from Part 1
WIREGUARD_PRIVATE_KEY=your_private_key_here

# ── Server Selection ─────────────────────────────
# Pick a country with good P2P support
# Options: Netherlands, Switzerland, Sweden, Iceland, etc.
SERVER_COUNTRIES=Netherlands
```

### Docker Compose file

Create `/volume1/docker/media-stack/docker-compose.yml`:

```yaml
services:

  # ═══════════════════════════════════════════════════
  # VPN — ProtonVPN via Wireguard with port forwarding
  # ═══════════════════════════════════════════════════
  gluetun:
    image: qmcgaw/gluetun:v3.41.0
    container_name: gluetun
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      - VPN_SERVICE_PROVIDER=protonvpn
      - VPN_TYPE=wireguard
      - WIREGUARD_PRIVATE_KEY=${WIREGUARD_PRIVATE_KEY}
      - SERVER_COUNTRIES=${SERVER_COUNTRIES}
      # Port forwarding (ProtonVPN NAT-PMP)
      - VPN_PORT_FORWARDING=on
      - VPN_PORT_FORWARDING_PROVIDER=protonvpn
      # Only connect to servers that support port forwarding
      - PORT_FORWARD_ONLY=on
      - TZ=${TZ}
    ports:
      # qBittorrent WebUI
      - "8080:8080"
      # Torrent incoming connections
      - "6881:6881"
      - "6881:6881/udp"
    volumes:
      - /volume1/docker/media-stack/config/gluetun:/gluetun
    sysctls:
      - net.ipv6.conf.all.disable_ipv6=1
    restart: unless-stopped

  # ═══════════════════════════════════════════════════
  # Torrent Client — all traffic through VPN
  # ═══════════════════════════════════════════════════
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    network_mode: "service:gluetun"
    environment:
      - PUID=${PUID}
      - PGID=${PGID}
      - TZ=${TZ}
      - WEBUI_PORT=8080
    volumes:
      - /volume1/docker/media-stack/config/qbittorrent:/config
      - /volume1/media/downloads:/downloads
    depends_on:
      gluetun:
        condition: service_healthy
    restart: unless-stopped

  # ═══════════════════════════════════════════════════
  # Indexer Manager
  # ═══════════════════════════════════════════════════
  prowlarr:
    image: lscr.io/linuxserver/prowlarr:latest
    container_name: prowlarr
    environment:
      - PUID=${PUID}
      - PGID=${PGID}
      - TZ=${TZ}
    volumes:
      - /volume1/docker/media-stack/config/prowlarr:/config
    ports:
      - "9696:9696"
    restart: unless-stopped

  # ═══════════════════════════════════════════════════
  # TV Show Manager
  # ═══════════════════════════════════════════════════
  sonarr:
    image: lscr.io/linuxserver/sonarr:latest
    container_name: sonarr
    environment:
      - PUID=${PUID}
      - PGID=${PGID}
      - TZ=${TZ}
    volumes:
      - /volume1/docker/media-stack/config/sonarr:/config
      - /volume1/media/tv:/tv
      - /volume1/media/downloads:/downloads
    ports:
      - "8989:8989"
    restart: unless-stopped

  # ═══════════════════════════════════════════════════
  # Movie Manager
  # ═══════════════════════════════════════════════════
  radarr:
    image: lscr.io/linuxserver/radarr:latest
    container_name: radarr
    environment:
      - PUID=${PUID}
      - PGID=${PGID}
      - TZ=${TZ}
    volumes:
      - /volume1/docker/media-stack/config/radarr:/config
      - /volume1/media/movies:/movies
      - /volume1/media/downloads:/downloads
    ports:
      - "7878:7878"
    restart: unless-stopped
```

> **Why `v3.41.0` instead of `latest`?** The Gluetun `latest` tag is effectively a development branch and can be unstable. Pinning to a known stable version avoids surprises. You can check for newer stable releases at https://github.com/qdm12/gluetun/releases.

---

## Part 4: Launch

### Via SSH

```bash
cd /volume1/docker/media-stack
sudo docker compose up -d
```

### Via Container Manager UI

1. **Container Manager** → **Project** → **Create**
2. Project name: `media-stack`
3. Path: `/volume1/docker/media-stack`
4. Select "Use existing docker-compose.yml"
5. Click **Next** → **Done**

### Verify startup

```bash
# All containers should be running, gluetun should show "healthy"
sudo docker compose ps

# Check gluetun logs — look for:
#   "VPN is already running"
#   "Port forwarding is enabled"
#   "Forwarded port: XXXXX"
sudo docker logs gluetun

# Confirm VPN IP (should NOT be your real IP)
sudo docker exec gluetun wget -qO- https://ipinfo.io
```

---

## Part 5: Configure the Services

### Web UI addresses

| Service      | URL                              |
|------------- |--------------------------------- |
| qBittorrent  | `http://YOUR_SYNOLOGY_IP:8080`   |
| Prowlarr     | `http://YOUR_SYNOLOGY_IP:9696`   |
| Sonarr       | `http://YOUR_SYNOLOGY_IP:8989`   |
| Radarr       | `http://YOUR_SYNOLOGY_IP:7878`   |

### 1. qBittorrent

Get the temporary password:

```bash
sudo docker logs qbittorrent 2>&1 | grep "temporary password"
```

Log in with `admin` + temp password, then:

- **Settings → Downloads → Default Save Path**: `/downloads`
- **Settings → WebUI → Password**: change to something secure
- **Settings → Advanced → Network Interface**: leave as default (Gluetun handles this)
- **Settings → Connection → Listening Port**: Gluetun's port forwarding will handle this automatically. You can verify the forwarded port in the gluetun logs and optionally set it here.

**Important for port forwarding:** Add authentication bypass for the Docker bridge so Gluetun can update the listening port automatically:

- **Settings → WebUI → Authentication → Bypass authentication for clients in whitelisted IP subnets**: enable
- Add: `172.16.0.0/12` (Docker internal network range)

### 2. Prowlarr

- Complete the setup wizard (set authentication)
- **Indexers → Add Indexer** → add your preferred torrent indexers
- **Settings → Apps → Add Application**:
  - **Sonarr**: Prowlarr Server = `http://prowlarr:9696`, Sonarr Server = `http://sonarr:8989`, API Key from Sonarr Settings → General
  - **Radarr**: Prowlarr Server = `http://prowlarr:9696`, Radarr Server = `http://radarr:7878`, API Key from Radarr Settings → General
- Click **Sync App Indexers**

### 3. Sonarr

- **Settings → Media Management → Root Folders**: Add `/tv`
- **Settings → Download Clients → Add → qBittorrent**:
  - Host: `gluetun`
  - Port: `8080`
  - Username / Password: your qBittorrent credentials
- **Series → Add New**: search for any show by name

### 4. Radarr

- **Settings → Media Management → Root Folders**: Add `/movies`
- **Settings → Download Clients → Add → qBittorrent**:
  - Host: `gluetun`
  - Port: `8080`
  - Username / Password: your qBittorrent credentials
- **Movies → Add New**: search for any movie by name

---

## Part 6: Verify VPN & Port Forwarding

### Confirm VPN tunnel

```bash
# Should show a ProtonVPN IP in the Netherlands (or your chosen country)
sudo docker exec gluetun wget -qO- https://ipinfo.io
```

### Confirm port forwarding

```bash
# Look for "Forwarded port: XXXXX" in the logs
sudo docker logs gluetun 2>&1 | grep -i "forwarded port"
```

The forwarded port is also written to `/tmp/gluetun/forwarded_port` inside the container.

### Kill-switch test

```bash
# Stop VPN — qBittorrent should lose all internet access
sudo docker stop gluetun
sudo docker exec qbittorrent ping -c 3 google.com   # should FAIL
sudo docker start gluetun
```

### Torrent IP test

Use a torrent IP checking service (search for "torrent IP check") — download their test torrent in qBittorrent and verify only your ProtonVPN IP appears, not your real one.

---

## Part 7: Synology-Specific Notes

### Port conflicts

If port 8080 conflicts with another Synology service, change it in the compose:

```yaml
ports:
  - "8085:8080"   # access qBittorrent on :8085 instead
```

### Firewall

If Synology firewall is enabled (**Control Panel → Security → Firewall**), allow ports 8080, 9696, 8989, 7878 from your local network.

### DSM version

DSM 7.2+ natively supports Docker Compose projects in Container Manager. Older DSM 7.x may require SSH for `docker compose` commands.

---

## Part 8: Maintenance

### Update containers

```bash
cd /volume1/docker/media-stack
sudo docker compose pull
sudo docker compose up -d
```

Or in Container Manager: select project → **Action** → **Build**

> **For Gluetun:** before updating, check the release notes at https://github.com/qdm12/gluetun/releases. Update the version tag in the compose file rather than switching to `latest`.

### View logs

```bash
sudo docker compose logs -f              # all
sudo docker compose logs -f gluetun      # VPN only
sudo docker compose logs -f qbittorrent  # torrents only
```

### Backup configs

```bash
tar czf /volume1/media-stack-backup.tar.gz /volume1/docker/media-stack/config/
```

---

## Quick Reference

```
You (browser on any device)
  │
  ├── Sonarr (:8989) ── "I want Breaking Bad" ──┐
  ├── Radarr (:7878) ── "I want Inception" ──────┤
  │                                               │
  │   Prowlarr (:9696) ◄─── searches indexers ◄───┘
  │         │
  │         ▼ sends .torrent
  │   qBittorrent (:8080)
  │         │
  │         ▼ ALL traffic routed through
  │   Gluetun (ProtonVPN Wireguard + NAT-PMP port forwarding)
  │         │
  │         ▼
  │   Internet (your real IP is hidden)
  │         │
  │         ▼ downloaded files appear in
  └── /volume1/media/downloads → moved to /movies or /tv
```

---

## Troubleshooting

**Gluetun won't start / no healthy status**
- Check logs: `sudo docker logs gluetun`
- Verify the private key is correct and has no extra whitespace
- Make sure `/dev/net/tun` exists
- Try a different `SERVER_COUNTRIES` value

**Port forwarding not working**
- Confirm your ProtonVPN plan supports P2P and port forwarding
- Make sure you generated the Wireguard config with **NAT-PMP** enabled and **Moderate NAT** disabled
- Look for "Forwarded port" in gluetun logs
- The forwarded port may change on VPN reconnection — this is normal

**qBittorrent UI not loading**
- The UI is exposed through Gluetun's ports, so Gluetun must be healthy first
- Check if another service is using port 8080
- Try: `sudo docker logs qbittorrent`

**Slow speeds**
- Try a closer `SERVER_COUNTRIES` (geographic proximity matters)
- Verify port forwarding is active — without it, you can only connect to peers, not receive incoming connections
- Check qBittorrent connection limits in Settings → Connection

**Sonarr/Radarr can't connect to qBittorrent**
- Host should be `gluetun`, not `qbittorrent` or `localhost`
- Port should be `8080`
- Make sure qBittorrent credentials are correct
