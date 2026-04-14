package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"time"
)

func cmdCheckVPN(_ []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")
	env := loadEnvOrFatal(envFile)
	port := envDefault(env, "PELICULA_PORT", "7354")

	fmt.Printf("%sVPN & Service Health Check%s\n", colorBold, colorReset)
	fmt.Println()

	url := fmt.Sprintf("http://localhost:%s/api/pelicula/health", port)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fail("Could not reach middleware at " + url + " — is the stack running?")
		fmt.Println()
		info("Run: pelicula up")
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		fail("Could not parse health response: " + err.Error())
		return
	}

	// VPN checks
	vpn, _ := data["vpn"].(map[string]interface{})
	vpnIP, _ := vpn["ip"].(string)
	vpnCountry, _ := vpn["country"].(string)
	vpnStatus, _ := vpn["status"].(string)
	vpnPort, _ := vpn["port"].(float64)

	if vpnStatus == "healthy" && vpnIP != "" {
		label := vpnIP
		if vpnCountry != "" {
			label = vpnIP + " (" + vpnCountry + ")"
		}
		pass("VPN tunnel: " + label)
	} else {
		fail("VPN tunnel: not connected")
	}

	if vpnIP != "" {
		pass("VPN IP: " + vpnIP)
	} else {
		fail("VPN IP: not available")
	}

	if vpnPort > 0 {
		pass(fmt.Sprintf("Port forwarding: port %.0f", vpnPort))
	} else {
		// Pull watchdog diagnostic details if available.
		wd, _ := vpn["watchdog"].(map[string]interface{})
		wdStatus, _ := wd["status"].(string)
		wdTunnel, _ := wd["tunnel_status"].(string)
		wdCooldown, _ := wd["cooldown_remaining"].(float64)
		wdConsec, _ := wd["consecutive_zero"].(float64)
		wdGrace, _ := wd["grace_remaining"].(float64)

		switch wdStatus {
		case "grace":
			minsLeft := int(wdGrace) / 2 // 30s polls → rough minutes at 2 polls/min
			fail(fmt.Sprintf("Port forwarding: not active (grace period, %.0f/10 polls, ~%d min to restart)", wdConsec, minsLeft))
		case "restarting":
			fail(fmt.Sprintf("Port forwarding: not active (restarting, cooldown %.0f ticks remaining)", wdCooldown))
		case "degraded":
			fail("Port forwarding: not active (degraded — manual restart required)")
		default:
			fail("Port forwarding: not active")
		}
		if wdTunnel != "" {
			info(fmt.Sprintf("VPN tunnel status: %s", wdTunnel))
		}
	}

	// Service checks
	fmt.Println()
	services, _ := data["services"].(map[string]interface{})
	if services != nil {
		names := make([]string, 0, len(services))
		for k := range services {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			status, _ := services[name].(string)
			label := string([]byte{name[0] - 32}) + name[1:] // capitalize
			if status == "up" {
				pass(label + ": reachable")
			} else {
				fail(label + ": not reachable")
			}
		}
	}

	// Summary
	fmt.Println()
	passed, _ := data["checks_passed"].(float64)
	total, _ := data["checks_total"].(float64)
	color := colorRed
	if passed == total {
		color = colorGreen
	} else if passed > 0 {
		color = colorYellow
	}
	fmt.Printf("  %s%s%.0f/%.0f checks passed%s\n", color, colorBold, passed, total, colorReset)
}
