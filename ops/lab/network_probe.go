// Standalone probe for check_network.py; not included in application images.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "not-ready" {
		os.Exit(1)
	}
	source := os.Args[1]
	failures := 0
	check := func(name string, pass bool) {
		fmt.Printf("%s %s pass=%t\n", source, name, pass)
		if !pass {
			failures++
		}
	}
	for _, target := range []struct {
		name, address string
		allow         bool
	}{
		{"identity-rpc", "identity:8443", source == "gateway"},
		{"database", "database-rw:5432", source == "identity"},
		{"google-proxy", "google-egress:3128", source == "identity"},
		{"direct-internet", "www.googleapis.com:443", false},
	} {
		connection, err := net.DialTimeout("tcp", target.address, 2*time.Second)
		if connection != nil {
			connection.Close()
		}
		check(target.name, (err == nil) == target.allow)
	}
	if source == "identity" {
		proxy, _ := url.Parse("http://google-egress:3128")
		status := 0
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), OnProxyConnectResponse: func(_ context.Context, _ *url.URL, _ *http.Request, r *http.Response) error {
			status = r.StatusCode
			return nil
		}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
		response, err := client.Get("https://www.googleapis.com/oauth2/v3/certs")
		allowed := err == nil && response.StatusCode == 200
		if response != nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		check("google-jwks-allowed", allowed)
		status = 0
		response, err = client.Get("https://example.com/")
		if response != nil {
			response.Body.Close()
		}
		check("other-host-denied", err != nil && status == 403)
	}
	if failures != 0 {
		os.Exit(1)
	}
}
