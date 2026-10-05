package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// gluetunEndpoints are the control-API paths check-vpn queries. The forwarded
// port moved from /v1/openvpn/portforwarded to /v1/portforward; both are
// tried, in that order.
const (
	gluetunIPPath      = "/v1/publicip/ip"
	gluetunPortPath    = "/v1/openvpn/portforwarded"
	gluetunPortPathNew = "/v1/portforward"
)

// gluetunGetArgs builds the `compose exec` arguments that GET a gluetun
// control-API path from inside the gluetun container.
func gluetunGetArgs(user, pass, path string) []string {
	return []string{
		"exec", "-T", "gluetun",
		"wget", "-qO-", "--user=" + user, "--password=" + pass,
		"http://localhost:8000" + path,
	}
}

// jsonValue extracts key from a flat JSON object response such as
// {"public_ip":"1.2.3.4"} or {"port":51234}. Anything that is not such an
// object (or lacks the key) comes back as the trimmed raw text.
func jsonValue(raw, key string) string {
	raw = strings.TrimSpace(raw)
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return raw
	}
	if v, ok := obj[key]; ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return raw
}

// cmdCheckVPN prints the tunnel's public IP and forwarded port as seen from
// inside the gluetun container.
func cmdCheckVPN(ctx *Context, _ []string) {
	ctx.LoadEnv()
	if !vpnEnabled(ctx.Env) {
		fatal("No WIREGUARD_PRIVATE_KEY in .env — the VPN is not configured")
	}
	c := ctx.compose(false)
	user := envDefault(ctx.Env, "GLUETUN_HTTP_USER", "pelicula")
	pass := ctx.Env["GLUETUN_HTTP_PASS"]

	fmt.Printf("%sVPN check%s\n\n", colorBold, colorReset)

	get := func(path string) (string, bool) {
		out, err := c.Output(gluetunGetArgs(user, pass, path)...)
		return strings.TrimSpace(string(out)), err == nil
	}

	failed := false
	if out, good := get(gluetunIPPath); good && out != "" {
		ok("Public IP: " + jsonValue(out, "public_ip"))
	} else {
		failed = true
		fail("Public IP: not available — is gluetun running? (pelicula logs gluetun)")
	}

	out, good := get(gluetunPortPath)
	if !good || out == "" {
		out, good = get(gluetunPortPathNew)
	}
	if port := jsonValue(out, "port"); good && port != "" && port != "0" {
		ok("Forwarded port: " + port)
	} else {
		failed = true
		fail("Forwarded port: not available yet (port forwarding can take a minute after connect)")
	}

	if failed {
		fatal("VPN check failed")
	}
}
