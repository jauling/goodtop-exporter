package main

import (
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Config perfectly matches the original format expected in config.yaml without PoE
type Config struct {
	Address         string `yaml:"address"`
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	PollRateSeconds int    `yaml:"poll_rate_seconds"`
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
}

// GoodtopCollector manages the session state and internal thread-safe metrics cache
type GoodtopCollector struct {
	config  *Config
	client  *http.Client
	baseURL string
	mu      sync.RWMutex

	// Internal data state tracking
	up                float64
	sysUptime         float64
	devInfo           map[string]string
	portState         map[string]float64
	portLinkStatus    map[string]float64
	portTxGoodPkt     map[string]float64
	portRxGoodPkt     map[string]float64
	portTxGoodBytes   map[string]float64
	portRxGoodBytes   map[string]float64
	portSpeed         map[string]float64
	portDuplex        map[string]float64
	portFlowControl   map[string]float64

	// Prometheus Metrics Descriptors
	upDesc              *prometheus.Desc
	sysUptimeDesc       *prometheus.Desc
	devInfoDesc         *prometheus.Desc
	portStateDesc       *prometheus.Desc
	portLinkStatusDesc  *prometheus.Desc
	portTxGoodPktDesc   *prometheus.Desc
	portRxGoodPktDesc   *prometheus.Desc
	portTxGoodBytesDesc *prometheus.Desc
	portRxGoodBytesDesc *prometheus.Desc
	portSpeedDesc       *prometheus.Desc
	portDuplexDesc      *prometheus.Desc
	portFlowControlDesc *prometheus.Desc
}

func NewGoodtopCollector(cfg *Config) (*GoodtopCollector, error) {
	addr := cfg.Address
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimSuffix(addr, "/")

	return &GoodtopCollector{
		config:          cfg,
		baseURL:         addr,
		devInfo:         make(map[string]string),
		portState:       make(map[string]float64),
		portLinkStatus:  make(map[string]float64),
		portTxGoodPkt:   make(map[string]float64),
		portRxGoodPkt:   make(map[string]float64),
		portTxGoodBytes: make(map[string]float64),
		portRxGoodBytes: make(map[string]float64),
		portSpeed:       make(map[string]float64),
		portDuplex:      make(map[string]float64),
		portFlowControl: make(map[string]float64),

		upDesc: prometheus.NewDesc(
			"goodtop_up",
			"Whether the goodtop switch scrape was successful (1) or failed (0)",
			nil, nil,
		),
		sysUptimeDesc: prometheus.NewDesc(
			"goodtop_sys_uptime_seconds",
			"System uptime of the switch appliance in seconds",
			nil, nil,
		),
		devInfoDesc: prometheus.NewDesc(
			"goodtop_device_info",
			"Hardware and firmware metadata labels for the switch asset",
			[]string{"device_name", "model", "firmware_version", "ip_address", "netmask", "mac_address"}, nil,
		),
		portStateDesc: prometheus.NewDesc(
			"goodtop_port_state",
			"Port administrative enabled state (1 = Enable, 0 = Disable)",
			[]string{"port"}, nil,
		),
		portLinkStatusDesc: prometheus.NewDesc(
			"goodtop_port_link_status",
			"Port link operational status (1 = Link Up, 0 = Link Down)",
			[]string{"port"}, nil,
		),
		portTxGoodPktDesc: prometheus.NewDesc(
			"goodtop_port_tx_good_pkt",
			"Transmitted good packets count",
			[]string{"port"}, nil,
		),
		portRxGoodPktDesc: prometheus.NewDesc(
			"goodtop_port_rx_good_pkt",
			"Received good packets count",
			[]string{"port"}, nil,
		),
		portTxGoodBytesDesc: prometheus.NewDesc(
			"goodtop_port_tx_good_bytes",
			"Transmitted good bytes count",
			[]string{"port"}, nil,
		),
		portRxGoodBytesDesc: prometheus.NewDesc(
			"goodtop_port_rx_good_bytes",
			"Received good bytes count",
			[]string{"port"}, nil,
		),
		portSpeedDesc: prometheus.NewDesc(
			"goodtop_port_speed_mbps",
			"Configured or negotiated port interface link speed in Mbps",
			[]string{"port"}, nil,
		),
		portDuplexDesc: prometheus.NewDesc(
			"goodtop_port_duplex",
			"Port operational duplex mode status (2 = Full, 1 = Half, 0 = Auto/Down)",
			[]string{"port"}, nil,
		),
		portFlowControlDesc: prometheus.NewDesc(
			"goodtop_port_flow_control",
			"Port flow control operational status (1 = On, 0 = Off)",
			[]string{"port"}, nil,
		),
	}, nil
}

func (c *GoodtopCollector) computeMD5Hash() string {
	hasher := md5.New()
	hasher.Write([]byte(c.config.Username + c.config.Password))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (c *GoodtopCollector) login() error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("failed to create cookie jar: %w", err)
	}

	timeoutSec := c.config.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 5
	}

	c.client = &http.Client{
		Jar:     jar,
		Timeout: time.Duration(timeoutSec) * time.Second,
	}

	md5Hash := c.computeMD5Hash()
	parsedURL, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}

	cookies := []*http.Cookie{
		{
			Name:  "admin",
			Value: md5Hash,
			Path:  "/",
		},
	}
	c.client.Jar.SetCookies(parsedURL, cookies)

	formData := url.Values{}
	formData.Set("username", c.config.Username)
	formData.Set("password", c.config.Password)
	formData.Set("Response", md5Hash)
	formData.Set("language", "EN")

	loginURL := fmt.Sprintf("%s/login.cgi", c.baseURL)
	req, err := http.NewRequest("POST", loginURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to build auth payload structure: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", c.baseURL+"/")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("network error during POST authentication: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication rejected with status code %d", resp.StatusCode)
	}

	log.Println("Successfully authenticated session with Goodtop switch")
	return nil
}

func (c *GoodtopCollector) StartPollingLoop() {
	pollInterval := time.Duration(c.config.PollRateSeconds) * time.Second
	if pollInterval <= 0 {
		pollInterval = 10 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	c.scrape()

	go func() {
		for range ticker.C {
			c.scrape()
		}
	}()
}

func (c *GoodtopCollector) executeGetRequest(endpoint string, referer string) (string, error) {
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", referer)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || strings.Contains(resp.Request.URL.Path, "login") {
		return "", fmt.Errorf("session expired or unauthorized redirect caught")
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(bodyBytes), nil
}

func (c *GoodtopCollector) scrape() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		if err := c.login(); err != nil {
			log.Printf("Authentication failed: %v", err)
			c.up = 0
			return
		}
	}

	// 1. Scrape Port Counters Page
	statsURL := fmt.Sprintf("%s/port.cgi?page=stats", c.baseURL)
	statsHTML, err := c.executeGetRequest(statsURL, c.baseURL+"/main.html")
	if err != nil {
		log.Println("Port stats call failed. Re-authenticating session cookie context...")
		if err := c.login(); err != nil {
			log.Printf("Re-authentication cycle dropped: %v", err)
			c.up = 0
			return
		}
		statsHTML, err = c.executeGetRequest(statsURL, c.baseURL+"/main.html")
		if err != nil {
			log.Printf("Terminal execution failure pulling statistical data maps: %v", err)
			c.up = 0
			return
		}
	}

	// 2. Scrape System Info Page
	infoURL := fmt.Sprintf("%s/info.cgi", c.baseURL)
	infoHTML, err := c.executeGetRequest(infoURL, c.baseURL+"/main.html")
	if err != nil {
		log.Printf("System Info scraping pipeline dropped execution steps: %v", err)
		// Don't kill the loop completely if info page fails once, but clear up status if fatal
	}

	c.up = 1
	c.parseStatsMetrics(statsHTML)
	if infoHTML != "" {
		c.parseInfoMetrics(infoHTML)
	}
}

func parseSplitCounter(raw string) float64 {
	parts := strings.Split(strings.TrimSpace(raw), "-")
	if len(parts) != 2 {
		val, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return 0
		}
		return val
	}
	high, _ := strconv.ParseFloat(parts[0], 64)
	low, _ := strconv.ParseFloat(parts[1], 64)
	return (high * 4294967296) + low
}

// Converts layout sequences like "9Day1Hour49Minute56Second" cleanly into floating seconds
func parseUptime(raw string) float64 {
	re := regexp.MustCompile(`(?:(\d+)\s*Day)?\s*(?:(\d+)\s*Hour)?\s*(?:(\d+)\s*Minute)?\s*(?:(\d+)\s*Second)?`)
	matches := re.FindStringSubmatch(raw)
	if matches == nil {
		return 0
	}
	var totalSeconds float64
	if matches[1] != "" {
		d, _ := strconv.ParseFloat(matches[1], 64)
		totalSeconds += d * 86400
	}
	if matches[2] != "" {
		h, _ := strconv.ParseFloat(matches[2], 64)
		totalSeconds += h * 3600
	}
	if matches[3] != "" {
		m, _ := strconv.ParseFloat(matches[3], 64)
		totalSeconds += m * 60
	}
	if matches[4] != "" {
		s, _ := strconv.ParseFloat(matches[4], 64)
		totalSeconds += s
	}
	return totalSeconds
}

// Converts port text speeds (e.g., "2500M", "1000M") cleanly into absolute Mbps values
func parseSpeed(raw string) float64 {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if strings.HasSuffix(raw, "m") {
		val, _ := strconv.ParseFloat(strings.TrimSuffix(raw, "m"), 64)
		return val
	}
	if strings.HasSuffix(raw, "g") {
		val, _ := strconv.ParseFloat(strings.TrimSuffix(raw, "g"), 64)
		return val * 1000
	}
	return 0 // "Auto" or Down returns 0 Mbps
}

func (c *GoodtopCollector) parseStatsMetrics(htmlContent string) {
	rowRegex := regexp.MustCompile(`(?is)<tr[^>]*>\s*(.*?)\s*</tr>`)
	cellRegex := regexp.MustCompile(`(?is)<td[^>]*>\s*(.*?)\s*</td>`)

	rows := rowRegex.FindAllStringSubmatch(htmlContent, -1)
	for _, rowMatch := range rows {
		if len(rowMatch) < 2 {
			continue
		}
		cells := cellRegex.FindAllStringSubmatch(rowMatch[1], -1)
		if len(cells) != 7 {
			continue
		}

		portName := strings.TrimSpace(cells[0][1])
		if strings.ToLower(portName) == "port" || portName == "" {
			continue
		}

		stateRaw := strings.ToLower(strings.TrimSpace(cells[1][1]))
		linkRaw := strings.ToLower(strings.TrimSpace(cells[2][1]))

		stateVal := 0.0
		if stateRaw == "enable" {
			stateVal = 1.0
		}
		linkVal := 0.0
		if linkRaw == "link up" {
			linkVal = 1.0
		}

		c.portState[portName] = stateVal
		c.portLinkStatus[portName] = linkVal
		c.portTxGoodPkt[portName] = parseSplitCounter(cells[3][1])
		c.portRxGoodPkt[portName] = parseSplitCounter(cells[4][1])
		c.portTxGoodBytes[portName] = parseSplitCounter(cells[5][1])
		c.portRxGoodBytes[portName] = parseSplitCounter(cells[6][1])
	}
}

func (c *GoodtopCollector) parseInfoMetrics(htmlContent string) {
	// 1. Tag-Agnostic Metadata Extraction for Device Info Table
	nameRegex := regexp.MustCompile(`(?is)name="devName"\s+value="([^"]+)"`)
	modelRegex := regexp.MustCompile(`(?is)Device\s+Model:.*?</t[db]>\s*<td[^>]*>([^<]+)</td>`)
	fwRegex := regexp.MustCompile(`(?is)Firmware\s+Version:.*?</t[db]>\s*<td[^>]*>([^<]+)</td>`)
	ipRegex := regexp.MustCompile(`(?is)IP\s+Address:.*?</t[db]>\s*<td[^>]*>([^<]+)</td>`)
	netmaskRegex := regexp.MustCompile(`(?is)Netmask:.*?</t[db]>\s*<td[^>]*>([^<]+)</td>`)
	macRegex := regexp.MustCompile(`(?is)MAC\s+Address:.*?</t[db]>\s*<td[^>]*>([^<]+)</td>`)
	uptimeRegex := regexp.MustCompile(`(?is)Sys\s+Uptime:.*?</t[db]>\s*<td[^>]*>([^<]+)</td>`)

	if m := nameRegex.FindStringSubmatch(htmlContent); len(m) > 1 { c.devInfo["device_name"] = strings.TrimSpace(m[1]) }
	if m := modelRegex.FindStringSubmatch(htmlContent); len(m) > 1 { c.devInfo["model"] = strings.TrimSpace(m[1]) }
	if m := fwRegex.FindStringSubmatch(htmlContent); len(m) > 1 { c.devInfo["firmware_version"] = strings.TrimSpace(m[1]) }
	if m := ipRegex.FindStringSubmatch(htmlContent); len(m) > 1 { c.devInfo["ip_address"] = strings.TrimSpace(m[1]) }
	if m := netmaskRegex.FindStringSubmatch(htmlContent); len(m) > 1 { c.devInfo["netmask"] = strings.TrimSpace(m[1]) }
	if m := macRegex.FindStringSubmatch(htmlContent); len(m) > 1 { c.devInfo["mac_address"] = strings.TrimSpace(m[1]) }

	if m := uptimeRegex.FindStringSubmatch(htmlContent); len(m) > 1 {
		c.sysUptime = parseUptime(m[1])
	}

	// 2. Port Status Table Extraction (5 Columns: Port, Link, Duplex, Speed, Flow Control)
	rowRegex := regexp.MustCompile(`(?is)<tr[^>]*>\s*(.*?)\s*</tr>`)
	cellRegex := regexp.MustCompile(`(?is)<td[^>]*>\s*(.*?)\s*</td>`)

	rows := rowRegex.FindAllStringSubmatch(htmlContent, -1)
	for _, rowMatch := range rows {
		if len(rowMatch) < 2 {
			continue
		}
		cells := cellRegex.FindAllStringSubmatch(rowMatch[1], -1)
		if len(cells) != 5 {
			continue
		}

		portName := strings.TrimSpace(cells[0][1])
		if strings.ToLower(portName) == "port" || portName == "" {
			continue
		}

		duplexRaw := strings.ToLower(strings.TrimSpace(cells[2][1]))
		speedRaw := strings.ToLower(strings.TrimSpace(cells[3][1]))
		fcRaw := strings.ToLower(strings.TrimSpace(cells[4][1]))

		duplexVal := 0.0
		if strings.Contains(duplexRaw, "full") {
			duplexVal = 2.0
		} else if strings.Contains(duplexRaw, "half") {
			duplexVal = 1.0
		}

		fcVal := 0.0
		if fcRaw == "on" {
			fcVal = 1.0
		}

		c.portSpeed[portName] = parseSpeed(speedRaw)
		c.portDuplex[portName] = duplexVal
		c.portFlowControl[portName] = fcVal
	}
}

func (c *GoodtopCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.upDesc
	ch <- c.sysUptimeDesc
	ch <- c.devInfoDesc
	ch <- c.portStateDesc
	ch <- c.portLinkStatusDesc
	ch <- c.portTxGoodPktDesc
	ch <- c.portRxGoodPktDesc
	ch <- c.portTxGoodBytesDesc
	ch <- c.portRxGoodBytesDesc
	ch <- c.portSpeedDesc
	ch <- c.portDuplexDesc
	ch <- c.portFlowControlDesc
}

func (c *GoodtopCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, c.up)
	ch <- prometheus.MustNewConstMetric(c.sysUptimeDesc, prometheus.GaugeValue, c.sysUptime)

	// Send single static label metadata block info block
	ch <- prometheus.MustNewConstMetric(
		c.devInfoDesc,
		prometheus.GaugeValue,
		1.0,
		c.devInfo["device_name"],
		c.devInfo["model"],
		c.devInfo["firmware_version"],
		c.devInfo["ip_address"],
		c.devInfo["netmask"],
		c.devInfo["mac_address"],
	)

	for port, val := range c.portState {
		ch <- prometheus.MustNewConstMetric(c.portStateDesc, prometheus.GaugeValue, val, port)
	}
	for port, val := range c.portLinkStatus {
		ch <- prometheus.MustNewConstMetric(c.portLinkStatusDesc, prometheus.GaugeValue, val, port)
	}
	for port, val := range c.portTxGoodPkt {
		ch <- prometheus.MustNewConstMetric(c.portTxGoodPktDesc, prometheus.CounterValue, val, port)
	}
	for port, val := range c.portRxGoodPkt {
		ch <- prometheus.MustNewConstMetric(c.portRxGoodPktDesc, prometheus.CounterValue, val, port)
	}
	for port, val := range c.portTxGoodBytes {
		ch <- prometheus.MustNewConstMetric(c.portTxGoodBytesDesc, prometheus.CounterValue, val, port)
	}
	for port, val := range c.portRxGoodBytes {
		ch <- prometheus.MustNewConstMetric(c.portRxGoodBytesDesc, prometheus.CounterValue, val, port)
	}
	for port, val := range c.portSpeed {
		ch <- prometheus.MustNewConstMetric(c.portSpeedDesc, prometheus.GaugeValue, val, port)
	}
	for port, val := range c.portDuplex {
		ch <- prometheus.MustNewConstMetric(c.portDuplexDesc, prometheus.GaugeValue, val, port)
	}
	for port, val := range c.portFlowControl {
		ch <- prometheus.MustNewConstMetric(c.portFlowControlDesc, prometheus.GaugeValue, val, port)
	}
}

func main() {
	configPath := flag.String("config.file", "config.yaml", "Path to the configuration file.")
	listenAddress := flag.String("web.listen-address", ":8080", "Address to listen on for web interface and telemetry.")
	metricsPath := flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	flag.Parse()

	yamlFile, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(yamlFile, &cfg); err != nil {
		log.Fatalf("Error unmarshalling configuration data: %v", err)
	}

	if cfg.Address == "" || cfg.Username == "" || cfg.Password == "" {
		log.Fatalf("Missing critical definitions inside configuration profile (address/username/password)")
	}

	collector, err := NewGoodtopCollector(&cfg)
	if err != nil {
		log.Fatalf("Failed setup initialization requirements: %v", err)
	}
	prometheus.MustRegister(collector)

	collector.StartPollingLoop()

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
			<head><title>Goodtop Switch Exporter</title></head>
			<body>
			<h1>Goodtop Switch Exporter</h1>
			<p><a href="` + *metricsPath + `">Metrics Target</a></p>
			</body>
			</html>`))
	})

	log.Printf("Starting goodtop-exporter server instance at %s", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, nil); err != nil {
		log.Fatalf("HTTP execution daemon crashed: %v", err)
	}
}
