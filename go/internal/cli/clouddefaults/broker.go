package clouddefaults

import (
	"net"
	"strings"
)

func BrokerURL(cloudGRPC, brokerURL, defaultBrokerPort string) string {
	if brokerURL != "" {
		return brokerURL
	}
	if strings.HasSuffix(cloudGRPC, ":443") {
		return cloudGRPC
	}
	host := cloudGRPC
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return net.JoinHostPort(host, defaultBrokerPort)
}
