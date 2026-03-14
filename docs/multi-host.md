# Multi-Host Setup with Tailscale / WireGuard

guppi supports connecting multiple machines together so you can monitor and interact with tmux sessions across all your hosts from a single dashboard. This guide covers how to set it up effectively using Tailscale or any WireGuard-based VPN.

## How It Works

guppi uses a star topology:

```
                    ┌──────────┐
          ┌────────►│   Hub    │◄────────┐
          │         │ (desktop)│         │
          │         └──────────┘         │
          │                              │
     ┌────┴─────┐                 ┌──────┴───┐
     │  Peer A  │                 │  Peer B   │
     │ (laptop) │                 │ (server)  │
     └──────────┘                 └───────────┘
```

- **Hub**: One node acts as the coordinator. It aggregates session state from all peers and serves the combined dashboard.
- **Peers**: Other nodes connect to the hub and share their tmux session state. Terminal streams (PTY data) are relayed through the hub on demand.
- **mTLS**: All peer-to-hub communication uses mutual TLS with auto-generated certificates and ed25519 identity keys.

## Why Tailscale / WireGuard?

A VPN overlay network solves several problems at once:

1. **No port forwarding** — Nodes communicate over private IPs regardless of NAT, firewalls, or ISP restrictions.
2. **Encrypted transport** — WireGuard encrypts all traffic at the network layer. Combined with guppi's mTLS, you get defense in depth.
3. **Stable hostnames** — Tailscale provides MagicDNS names (e.g., `desktop.ts.net`) that follow machines across networks.
4. **ACLs** — Tailscale ACLs let you restrict which machines can talk to each other, adding another layer of access control on top of guppi's pairing-based auth.

## Setup with Tailscale

### Prerequisites

- [Tailscale](https://tailscale.com/download) installed and authenticated on all machines
- `guppi` binary installed on all machines
- tmux running on all machines

### Step 1: Start the Hub

Pick one machine to be the hub (typically your primary workstation). Note its Tailscale hostname:

```bash
tailscale status
# 100.x.x.x  desktop        youruser@  linux  ...
```

Start guppi with TLS SANs that include the Tailscale hostname and IP:

```bash
guppi server --tls-san desktop.ts.net --tls-san 100.x.x.x
```

Or set via environment variable:

```bash
export GUPPI_TLS_SAN=desktop.ts.net,100.x.x.x
guppi server
```

The auto-generated TLS certificate will include these SANs, allowing peers to connect using the Tailscale hostname.

### Step 2: Pair Peers

On the hub, generate a pairing code:

```bash
guppi pair generate
# Pairing code: ABC123 (valid for 5 minutes)
```

On the peer machine, join the hub:

```bash
guppi pair join --hub https://desktop.ts.net:7654 --code ABC123
```

This exchanges ed25519 identity keys and establishes mutual trust. Once paired, the peer's identity is stored and it can reconnect automatically.

### Step 3: Start Peers

On each peer machine, start guppi pointing at the hub:

```bash
guppi server --hub https://desktop.ts.net:7654
```

The peer will connect to the hub, share its tmux session state, and relay PTY connections on demand.

#### Local-Only Mode

If you want a peer to participate in the network but only show its own sessions in its local dashboard:

```bash
guppi server --hub https://desktop.ts.net:7654 --local-only
```

The hub still sees all sessions. This is useful for machines where you don't need the full multi-host view locally.

### Step 4: Access the Dashboard

Open the hub's dashboard in your browser:

```
https://desktop.ts.net:7654
```

You'll see sessions from all connected peers. Clicking a remote session opens a PTY relay through the hub — the terminal stream flows from the peer through the hub to your browser.

## Setup with Generic WireGuard

If you're using raw WireGuard (without Tailscale), the setup is the same — just use the WireGuard tunnel IPs instead of Tailscale hostnames.

```bash
# Hub
guppi server --tls-san 10.0.0.1

# Peer
guppi server --hub https://10.0.0.1:7654
```

You'll need to handle DNS and key distribution yourself. The main differences from Tailscale:

- No MagicDNS — use IPs or configure DNS manually
- No automatic NAT traversal — ensure WireGuard peers can reach each other
- No centralized ACLs — use WireGuard's AllowedIPs and firewall rules

## systemd Services for Multi-Host

Extend the [systemd user services](tmux-setup.md#systemd-user-service) with hub configuration:

### Hub Service

```ini
[Unit]
Description=guppi web dashboard (hub)
After=tmux-server.service tailscaled.service
Requires=tmux-server.service
Wants=tailscaled.service

[Service]
Type=simple
ExecStart=%h/.local/bin/guppi server --tls-san %H.ts.net
Restart=on-failure
RestartSec=5
Environment=GUPPI_PORT=7654

[Install]
WantedBy=default.target
```

### Peer Service

```ini
[Unit]
Description=guppi web dashboard (peer)
After=tmux-server.service tailscaled.service
Requires=tmux-server.service
Wants=tailscaled.service

[Service]
Type=simple
ExecStart=%h/.local/bin/guppi server --hub https://desktop.ts.net:7654
Restart=on-failure
RestartSec=5
Environment=GUPPI_PORT=7654

[Install]
WantedBy=default.target
```

Note: `%H` in systemd expands to the machine's hostname. Adjust the hub address to match your actual hub's Tailscale hostname.

## TLS Configuration

### Auto-Generated Certificates (Default)

By default, guppi generates a self-signed ECDSA P-256 certificate on first run. The certificate is stored in the guppi config directory and reused across restarts.

Use `--tls-san` to add Subject Alternative Names so the certificate is valid for the hostnames/IPs peers use to connect:

```bash
guppi server --tls-san desktop.ts.net --tls-san 100.64.0.1 --tls-san desktop.local
```

### Custom Certificates

If you have certificates from a private CA or Let's Encrypt:

```bash
guppi server --tls-cert /path/to/cert.pem --tls-key /path/to/key.pem
```

### Using Tailscale's Built-in Certificates

Tailscale can issue certificates for your machines from a public CA via `tailscale cert`:

```bash
# Generate a cert for your machine's Tailscale FQDN
tailscale cert desktop.ts.net

# Use it with guppi
guppi server --tls-cert desktop.ts.net.crt --tls-key desktop.ts.net.key
```

This eliminates browser certificate warnings since the cert is signed by a trusted CA. Note that `tailscale cert` requires HTTPS to be enabled in your Tailscale admin console.

### Disabling TLS

If your VPN already provides encryption (WireGuard encrypts all traffic), you can optionally disable TLS:

```bash
guppi server --no-tls
```

However, keeping TLS enabled is recommended for defense in depth — it protects against local network sniffing and provides authentication at the application layer.

## Security Considerations

### Defense in Depth

With Tailscale + guppi, you get multiple layers of security:

| Layer | Protection |
|-------|-----------|
| WireGuard (Tailscale) | Network-level encryption, NAT traversal, private IPs |
| Tailscale ACLs | Which machines can communicate |
| guppi mTLS | Peer authentication via ed25519 keys + TLS certificates |
| guppi pairing | One-time code exchange to establish trust |
| guppi auth | Password-based login for the web dashboard |

### Recommendations

- **Always use a VPN** for multi-host setups. Don't expose guppi directly to the internet.
- **Keep pairing codes short-lived** — they expire after 5 minutes by default.
- **Use `--local-only`** on machines that don't need to see remote sessions.
- **Enable lingering** (`loginctl enable-linger`) on headless servers so guppi stays running.
- **Set a strong password** for the web dashboard, especially if accessing it remotely.
- **Use Tailscale ACLs** to restrict which machines can reach the hub's port.

## Troubleshooting

### Peer won't connect to hub

1. Verify Tailscale connectivity: `tailscale ping desktop.ts.net`
2. Check the hub is listening: `curl -k https://desktop.ts.net:7654/api/health`
3. Verify pairing was completed: `guppi peers list`
4. Check logs: `journalctl --user -u guppi.service -f`

### Certificate errors

If peers get TLS errors connecting to the hub:

- Ensure the hub was started with the correct `--tls-san` values
- Delete the auto-generated cert and restart to regenerate: the cert is stored in the guppi config directory
- Use `--insecure` temporarily for debugging (not recommended for production)

### Sessions not appearing

- Ensure tmux is running on the peer: `tmux list-sessions`
- Check that the peer isn't in `--local-only` mode if you expect to see its sessions on the hub
- Verify the peer shows as connected: `guppi peers list` on the hub
