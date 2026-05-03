<p align="center">
  <img src="https://img.shields.io/github/license/veloriba/mygrok?style=for-the-badge&color=blue" />
  <img src="https://img.shields.io/github/go-mod/go-version/veloriba/mygrok?style=for-the-badge&color=00ADD8" />
  <img src="https://img.shields.io/github/stars/veloriba/mygrok?style=for-the-badge&color=gold" />
</p>

# mygrok

A minimal, high-performance ngrok clone for personal use. Built with Go and powered by [yamux](https://github.com/hashicorp/yamux) for robust connection multiplexing and [httputil](https://pkg.go.dev/net/http/httputil) for reliable reverse proxying with **full WebSocket support**.

Expose your local development servers (Next.js, React, etc.) to the internet through your own VPS with a single command. Support for custom subdomains and automated TUI dashboard.

---

## ✨ Features

- **Personal Infrastructure**: Total control over your data and domain.
- **Multiplexed**: Multiple concurrent HTTP requests over a single TCP connection.
- **WebSocket & HMR Support**: Works perfectly with Next.js, Webpack HMR, and real-time apps.
- **Wildcard SSL Support**: Full HTTPS support using Let's Encrypt wildcard certificates.
- **TUI Dashboard**: Real-time request logging, status monitoring, and URL display.
- **Zero Dependencies**: Single binary for client and server.

## 🚀 Quick Start

### 1. Server Setup (Ubuntu VPS)

1.  **Build and Install**:
    Initialize your `config.mk` (copy from `config.mk.example`) and run:
    ```bash
    make server-install
    ```
    This will build the binary, deploy it to your VPS, and set up a systemd service.

2.  **Nginx & SSL Configuration**:
    Follow the instructions in the [Wiki/SSL Section] to obtain a wildcard certificate using `certbot` and configure Nginx to proxy traffic to port 8080.

### 2. Client Usage (MacOS/Linux)

1.  **Configure environment**:
    ```bash
    export MYGROK_SERVER="yourdomain.com:7000"
    export MYGROK_TOKEN="your-secret-token"
    ```
2.  **Expose a local port**:
    ```bash
    ./bin/mygrok http 3000 my-app
    ```
    Or using Makefile:
    ```bash
    make run PORT=3000 SUB=my-app
    ```

## 🛠 Makefile Commands

- `make build`: Build both client and server binaries.
- `make test`: Run integration and unit tests.
- `make server-install`: Deploy server to VPS.
- `make server-status`: Check remote service status.
- `make cert-renew`: Trigger manual wildcard certificate renewal.
- `make run PORT=3000 SUB=name`: Launch client using `config.mk` settings.

## 🛠 Configuration

| Flag | Environment Variable | Description |
| --- | --- | --- |
| `--server` | `MYGROK_SERVER` | **Required**. Server address (e.g. `yourdomain.com:7000`) |
| `--token` | `MYGROK_TOKEN` | **Required**. Authentication secret token |

## 🏗 Architecture

```text
[User Browser] -> [Nginx :443] -> [mygrok-server :8080]
                                         |
                                   (Yamux Stream)
                                         |
[mygrok-client] <- (Control :7000) ------'
       |
[Local App :3000] (Support for HTTP & WebSockets)
```

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 💖 Support the Project

- [**GitHub Sponsors**](https://github.com/sponsors/veloriba)
- [**Buy Me a Coffee**](https://www.buymeacoffee.com/veloriba)

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
