package platform

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"os"
	"strings"
)

const ServiceURIPrefix = "spiffe://human-worth/services/"

func ServiceName(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("missing peer")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.PeerCertificates) == 0 {
		return "", errors.New("unverified peer")
	}
	return certificateService(info.State.PeerCertificates[0])
}
func certificateService(cert *x509.Certificate) (string, error) {
	if len(cert.URIs) != 1 {
		return "", errors.New("invalid service identity")
	}
	value := cert.URIs[0].String()
	if !strings.HasPrefix(value, ServiceURIPrefix) {
		return "", errors.New("invalid service identity")
	}
	name := strings.TrimPrefix(value, ServiceURIPrefix)
	switch name {
	case "gateway", "identity", "content", "asset", "voting", "moderation", "challenge", "discovery", "challenge-worker":
		return name, nil
	}
	return "", errors.New("unknown service identity")
}
func TLS(certFile, keyFile, caFile, serverName string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.New("service certificate unavailable")
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, errors.New("service CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid service CA")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ClientCAs: roots}
	if serverName == "" {
		config.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		config.ServerName = serverName
		config.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return errors.New("unverified server")
			}
			service, err := certificateService(state.PeerCertificates[0])
			// 新增业务客户端后，证书中的服务身份须与本次目标服务一致。
			if err != nil || service != strings.Split(serverName, ".")[0] {
				return errors.New("wrong server identity")
			}
			return nil
		}
	}
	return config, nil
}
