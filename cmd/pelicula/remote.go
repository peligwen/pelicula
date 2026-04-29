package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

//go:embed templates/remote.yml.tmpl
var remoteTmpl string

var (
	reValidHostname = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)
	reValidEmail    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// RenderRemoteConfigs renders nginx remote vhost config and compose/docker-compose.remote.yml
// from .env values. If remote access is disabled, it writes a placeholder and removes
// the remote compose file.
func RenderRemoteConfigs(scriptDir string, env EnvMap) error {
	configDir := env["CONFIG_DIR"]

	remoteConf := filepath.Join(configDir, "nginx", "remote.conf")
	remoteCompose := filepath.Join(scriptDir, "compose", "docker-compose.remote.yml")

	// Ensure nginx config dir exists
	if err := os.MkdirAll(filepath.Join(configDir, "nginx"), 0755); err != nil {
		return err
	}

	if !isRemoteEnabled(env) {
		// Write empty placeholder so nginx bind-mount succeeds
		if err := os.WriteFile(remoteConf, []byte("# Remote access disabled\n"), 0644); err != nil {
			return err
		}
		// Remove remote compose file if present
		_ = os.Remove(remoteCompose)
		return nil
	}

	// Validate required vars
	hostname := env["REMOTE_HOSTNAME"]
	httpsPort := envDefault(env, "REMOTE_HTTPS_PORT", "8920")
	httpPort := envDefault(env, "REMOTE_HTTP_PORT", "80")
	peliculaPort := envDefault(env, "PELICULA_PORT", "7354")
	simpleMode := hostname == ""

	if err := validateRemotePorts(peliculaPort, httpPort, httpsPort, simpleMode); err != nil {
		return err
	}

	if simpleMode {
		certDir := filepath.Join(configDir, "certs", "remote")
		if err := os.MkdirAll(certDir, 0755); err != nil {
			return err
		}
		if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
			return err
		}
		simpleTmplPath := filepath.Join(scriptDir, "nginx", "remote-simple.conf.template")
		simpleBytes, err := os.ReadFile(simpleTmplPath)
		if err != nil {
			return fmt.Errorf("nginx/remote-simple.conf.template not found: %w", err)
		}
		if err := os.WriteFile(remoteConf, simpleBytes, 0644); err != nil {
			return err
		}
		pass(fmt.Sprintf("Rendered nginx remote vhost (simple mode, port %s)", httpsPort))
		return writeSimpleRemoteCompose(remoteCompose, httpsPort, certDir)
	}

	certMode := envDefault(env, "REMOTE_CERT_MODE", "self-signed")
	leEmail := env["REMOTE_LE_EMAIL"]
	leStaging := env["REMOTE_LE_STAGING"]

	if certMode == "letsencrypt" && leEmail == "" {
		return fmt.Errorf("Let's Encrypt mode requires REMOTE_LE_EMAIL to be set\nSet REMOTE_LE_EMAIL in your .env file and re-run: pelicula up")
	}

	// Cert directories
	certDir := filepath.Join(configDir, "certs", "remote")
	acmeDir := filepath.Join(configDir, "certs", "acme-webroot")
	leStateDir := filepath.Join(configDir, "certs", "letsencrypt")

	for _, d := range []string{certDir, acmeDir, leStateDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}

	// Provision cert
	switch certMode {
	case "self-signed":
		if err := SetupRemoteSelfSignedCert(certDir, hostname); err != nil {
			return err
		}
	case "byo":
		if _, err := os.Stat(filepath.Join(certDir, "fullchain.pem")); err != nil {
			return fmt.Errorf("BYO cert mode requires certificate files:\n  %s/fullchain.pem\n  %s/privkey.pem\nPlace your certificate there and re-run: pelicula up", certDir, certDir)
		}
		pass("BYO certificate found")
	case "letsencrypt":
		// Bootstrap with self-signed if no cert yet (nginx needs one to start)
		if _, err := os.Stat(filepath.Join(certDir, "fullchain.pem")); err != nil {
			if err2 := SetupRemoteSelfSignedCert(certDir, hostname); err2 != nil {
				return err2
			}
			warn("No cert yet — certbot will issue one on first run")
		}
	}

	// Render remote.conf from template
	templatePath := filepath.Join(scriptDir, "nginx", "remote.conf.template")
	tmplData, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("nginx/remote.conf.template not found: %w", err)
	}

	// Substitute ${REMOTE_HOSTNAME} and ${REMOTE_HTTPS_PORT} only (leave nginx $vars alone)
	replacer := strings.NewReplacer(
		"${REMOTE_HOSTNAME}", hostname,
		"${REMOTE_HTTPS_PORT}", httpsPort,
	)
	rendered := replacer.Replace(string(tmplData))

	if err := os.WriteFile(remoteConf, []byte(rendered), 0644); err != nil {
		return err
	}
	pass(fmt.Sprintf("Rendered nginx remote vhost (%s:%s)", hostname, httpsPort))

	// Write compose/docker-compose.remote.yml
	if err := writeRemoteCompose(remoteCompose, httpPort, httpsPort, certMode, leEmail, leStaging, hostname, certDir, acmeDir, leStateDir); err != nil {
		return err
	}

	return nil
}

// remoteComposeData holds the values interpolated into remote.yml.tmpl.
type remoteComposeData struct {
	HTTPPort    string
	HTTPSPort   string
	SimpleMode  bool
	LetsEncrypt bool
	LEStateDir  string
	CertDir     string
	AcmeDir     string
	StagingFlag string
	LEEmail     string
	Hostname    string
}

func writeRemoteCompose(path, httpPort, httpsPort, certMode, leEmail, leStaging, hostname, certDir, acmeDir, leStateDir string) error {
	if !reValidHostname.MatchString(hostname) {
		return fmt.Errorf("REMOTE_HOSTNAME %q is not a valid hostname — must contain only letters, digits, hyphens, and dots", hostname)
	}
	if certMode == "letsencrypt" && !reValidEmail.MatchString(leEmail) {
		return fmt.Errorf("REMOTE_LE_EMAIL %q is not a valid email address", leEmail)
	}

	stagingFlag := ""
	if leStaging == "true" {
		stagingFlag = "--staging"
	}

	data := remoteComposeData{
		HTTPPort:    httpPort,
		HTTPSPort:   httpsPort,
		SimpleMode:  false,
		LetsEncrypt: certMode == "letsencrypt",
		LEStateDir:  leStateDir,
		CertDir:     certDir,
		AcmeDir:     acmeDir,
		StagingFlag: stagingFlag,
		LEEmail:     leEmail,
		Hostname:    hostname,
	}

	tmpl, err := template.New("remote.yml").Parse(remoteTmpl)
	if err != nil {
		return fmt.Errorf("parse remote.yml.tmpl: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render remote.yml.tmpl: %w", err)
	}

	if certMode == "letsencrypt" {
		pass(fmt.Sprintf("Certbot sidecar configured (%s, staging: %s)", hostname, leStaging))
	}

	return os.WriteFile(path, buf.Bytes(), 0644)
}

// writeSimpleRemoteCompose renders docker-compose.remote.yml for simple mode
// (no HTTP port, no certbot, self-signed cert only).
func writeSimpleRemoteCompose(path, httpsPort, certDir string) error {
	data := remoteComposeData{
		HTTPSPort:  httpsPort,
		SimpleMode: true,
		CertDir:    certDir,
	}
	tmpl, err := template.New("remote.yml").Parse(remoteTmpl)
	if err != nil {
		return fmt.Errorf("parse remote.yml.tmpl: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render remote.yml.tmpl: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

// envDefault returns env[key] if non-empty, else defaultVal.
func envDefault(env EnvMap, key, defaultVal string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	return defaultVal
}

// validateRemotePorts rejects port-number shapes that compose would fail
// to bind: out-of-range values and host-port collisions between the main
// nginx listener (PELICULA_PORT) and the remote vhost. In simple mode the
// HTTP port is unused (no ACME, no redirect), so it isn't checked. Mirrors
// the validation in middleware/internal/app/settings/handler.go so both
// surfaces reject the same way.
func validateRemotePorts(peliculaPort, httpPort, httpsPort string, simpleMode bool) error {
	for _, p := range []struct{ name, val string }{
		{"PELICULA_PORT", peliculaPort},
		{"REMOTE_HTTPS_PORT", httpsPort},
	} {
		if err := checkPortRange(p.val); err != nil {
			return fmt.Errorf("%s %s", p.name, err)
		}
	}
	if !simpleMode {
		if err := checkPortRange(httpPort); err != nil {
			return fmt.Errorf("REMOTE_HTTP_PORT %s", err)
		}
		if httpPort == peliculaPort {
			return fmt.Errorf("REMOTE_HTTP_PORT must differ from PELICULA_PORT (%s)", peliculaPort)
		}
		if httpPort == httpsPort {
			return fmt.Errorf("REMOTE_HTTP_PORT and REMOTE_HTTPS_PORT must differ")
		}
	}
	if httpsPort == peliculaPort {
		return fmt.Errorf("REMOTE_HTTPS_PORT must differ from PELICULA_PORT (%s)", peliculaPort)
	}
	return nil
}

func checkPortRange(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q must be a valid port number (1-65535)", s)
	}
	return nil
}
