// Standalone probe for check_network.py; not included in application images.
package main

import (
	"context"
	"errors"
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
		{"identity-rpc", "identity:8443", source == "gateway" || source == "content"},
		{"content-rpc", "content:8443", source == "gateway"},
		{"database", "database-rw:5432", source == "identity" || source == "content"},
		{"google-proxy", "google-egress:3128", source == "identity"},
		{"direct-internet", "www.googleapis.com:443", false},
	} {
		// A fresh probe Pod can precede CNI/DNS convergence. A negative result
		// must be a TCP refusal/timeout, never a name-resolution failure.
		deadline := time.Now().Add(12 * time.Second)
		var err error
		var dnsError *net.DNSError
		for {
			var connection net.Conn
			connection, err = net.DialTimeout("tcp", target.address, 2*time.Second)
			if connection != nil {
				connection.Close()
			}
			dnsError = nil
			dnsFailed := errors.As(err, &dnsError)
			if err == nil || (!target.allow && !dnsFailed) || time.Now().After(deadline) {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		pass := dnsError == nil && (err == nil) == target.allow
		check(target.name, pass)
		if !pass {
			fmt.Printf("%s %s connection error: %v\n", source, target.name, err)
		}
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
