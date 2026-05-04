package ps

import (
	"fmt"
	"net/url"
)

// parseEndpointURL extracts the protocol and host from an endpoint URL.
// Expected formats: "s3://s3.amazonaws.com", "https://minio-service:9000"
func parseEndpointURL(endpointURL string) (protocol, host string, err error) {
	u, err := url.Parse(endpointURL)
	if err != nil {
		return "", "", fmt.Errorf("parse endpoint URL %q: %w", endpointURL, err)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("endpoint URL %q must include protocol and host (e.g. s3://... or https://...)", endpointURL)
	}
	return u.Scheme, u.Host, nil
}
