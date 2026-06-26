# 🔌 Goodtop Exporter

Prometheus Exporter for Goodtop GT-ST018M

## 📖 Overview

This Prometheus exporter retrieves device info, port statis and statistics from Goodtop GT-ST018M switch that lack SNMP functionality, enabling monitoring through a web-based interface. Also included is a Grafana dashboard that shows port status and packet count. Authentication page of the GT-ST018M is via `/login.cgi`.

## 🎯 Purpose

Many budget-friendly network switches do not support standard SNMP monitoring. This exporter provides a workaround by scraping port statistics directly from the switch's web interface. This exporter might work with other switches that use the RTL8373N or variants of this network chip. 

## 🖥️ Supported Devices

| Manufacturer | Model | Status | Contributor |
|--------------|-------|--------|-------------|
| Goodtop | GT-ST018M | ✅ Verified | @jauling |

## 🚀 Installation

### Prerequisites

- Go 1.23+
- Docker (optional)

### Direct Installation

1. Clone the repository
2. Download dependencies
```bash
go mod download
```

3. Copy configuration template
```bash
cp config.yaml.example config.yaml
```

4. Edit `config.yaml` with your switch details and parameters
5. Run the exporter
```bash
go run main.go
```

### Docker Deployment

```bash
# Build Docker image
docker build -t goodtop-exporter .

# Run container
docker run -v "./config.yaml:/config.yaml" -p 8080:8080 goodtop-exporter
```

## 📝 Configuration

Create a `config.yaml` with the following structure:

```yaml
address: "192.168.2.1"           # IP or hostname of the switch
username: "admin"                # Web interface username
password: "admin"                # Web interface password
poll_rate_seconds: 10            # Metrics polling interval
timeout_seconds: 5               # Request timeout
```

## 📊 Exposed Metrics

Metrics are collected via `/port.cgi?page=stats` and `/info.cgi`

- `goodtop_up`: Whether the goodtop switch scrape was successful (1) or failed (0
- `goodtop_device_info`: Switch information (device_name, firmware_version, ip_address, mac_address, model, netmask)
- `goodtop_sys_uptime_seconds`: System uptime of the switch appliance in seconds
- `goodtop_port_duplex`: Port operational duplex mode status (2 = Full, 1 = Half, 0 = Auto/Down)
- `goodtop_port_flow_control`: Port flow control operational status (1 = On, 0 = Off)
- `goodtop_port_link_status`: Port link operational status (1 = Link Up, 0 = Link Down)
- `goodtop_port_rx_good_bytes`: Received good bytes count
- `goodtop_port_rx_good_pkt`: Received good packets count
- `goodtop_port_speed_mbps`: Configured or negotiated port interface link speed in Mbps
- `goodtop_port_state`: Port administrative enabled state (1 = Enable, 0 = Disable)
- `goodtop_port_tx_good_bytes`: Transmitted good bytes count
- `goodtop_port_tx_good_pkt`: Transmitted good packets count

## 🤝 Contributing

1. Fork the repository
2. Create your feature branch
3. Commit your changes
4. Push to the branch
5. Create a new Pull Request

## 🚨 Limitations

- Requires web interface access to the switch
- Polling-based metrics collection
- Authentication via web interface credentials
- No TLS

## 📄 License

MIT License, see [LICENSE](LICENSE) file.

## 🐛 Issues

Report issues on the GitHub repository's issue tracker.
