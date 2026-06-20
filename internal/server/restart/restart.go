// Package restart provides the concrete updater.Restarter and
// updater.HealthChecker implementations for box daemons: an init-system
// service restart (systemd / OpenRC) and an HTTP health probe with retries.
package restart

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/updater"
)

// Compile-time assertions: both types satisfy the protocol interfaces.
var _ updater.Restarter = ServiceRestarter{}
var _ updater.HealthChecker = HTTPHealthChecker{}

// ServiceRestarter restarts an init-system service. distro/run are injected in
// tests; zero values resolve to live detection and exec.Command.
type ServiceRestarter struct {
	Service string
	distro  string
	run     func(name string, args ...string) error
}

// Restart restarts the service via the host init system.
func (s ServiceRestarter) Restart() error {
	distro := s.distro
	if distro == "" {
		distro = detectDistro()
	}
	run := s.run
	if run == nil {
		run = func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			return cmd.Run()
		}
	}
	switch distro {
	case "alpine":
		return run("rc-service", s.Service, "restart")
	default: // debian, rhel, and anything systemd-based
		return run("systemctl", "restart", s.Service)
	}
}

// detectDistro reads /etc/os-release ID / ID_LIKE and returns "alpine",
// "debian", "rhel", or "" (unknown → caller falls through to systemctl).
func detectDistro() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	id, idLike := osReleaseField(string(data), "ID"), osReleaseField(string(data), "ID_LIKE")
	switch id {
	case "alpine":
		return "alpine"
	case "debian", "ubuntu", "linuxmint", "pop", "elementary", "kali", "raspbian":
		return "debian"
	case "rhel", "fedora", "centos", "rocky", "almalinux", "ol", "amzn":
		return "rhel"
	}
	switch {
	case strings.Contains(idLike, "debian"), strings.Contains(idLike, "ubuntu"):
		return "debian"
	case strings.Contains(idLike, "rhel"), strings.Contains(idLike, "fedora"), strings.Contains(idLike, "centos"):
		return "rhel"
	}
	return ""
}

func osReleaseField(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.Trim(strings.TrimPrefix(line, key+"="), `"'`)
		}
	}
	return ""
}

// HTTPHealthChecker GETs URL and treats a status < 500 as healthy. It retries
// to absorb the brief window while a just-restarted service rebinds. Client
// lets the caller supply a UNIX-socket transport (server: admin socket) or a
// default client (web: TCP listen addr).
type HTTPHealthChecker struct {
	URL      string
	Client   *http.Client
	Retries  int           // total attempts (default 10)
	Interval time.Duration // delay between attempts (default 500ms)
}

// Healthy returns nil once URL responds with status < 500 within the retry
// budget, else the last error / a status error.
func (h HTTPHealthChecker) Healthy() error {
	retries, interval := h.Retries, h.Interval
	if retries <= 0 {
		retries = 10
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	var last error
	for i := 0; i < retries; i++ {
		if i > 0 {
			time.Sleep(interval)
		}
		resp, err := client.Get(h.URL)
		if err != nil {
			last = err
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 500 {
			return nil
		}
		last = fmt.Errorf("health %s: status %d", h.URL, resp.StatusCode)
	}
	if last == nil {
		last = fmt.Errorf("health %s: no response", h.URL)
	}
	return last
}
