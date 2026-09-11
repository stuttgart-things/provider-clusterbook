/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an HTTP client for the clusterbook REST API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// TLSOptions holds TLS configuration for the HTTP client.
type TLSOptions struct {
	// InsecureSkipVerify disables TLS certificate verification.
	InsecureSkipVerify bool
	// CustomCA is a PEM-encoded CA certificate to add to the trust pool.
	CustomCA string
}

// NewClient creates a new clusterbook API client.
func NewClient(baseURL string, opts *TLSOptions) (*Client, error) {
	transport := &http.Transport{}

	if opts != nil && (opts.InsecureSkipVerify || opts.CustomCA != "") {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // MinVersion is set

		if opts.InsecureSkipVerify {
			tlsConfig.InsecureSkipVerify = true //nolint:gosec // user-configured for self-signed certs
		}

		if opts.CustomCA != "" {
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM([]byte(opts.CustomCA)) {
				return nil, fmt.Errorf("cannot parse custom CA certificate")
			}
			tlsConfig.RootCAs = pool
		}

		transport.TLSClientConfig = tlsConfig
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}, nil
}

// IPInfo represents an IP address entry returned by the clusterbook API.
type IPInfo struct {
	IP      string `json:"IP"`
	Digit   string `json:"Digit"`
	Status  string `json:"Status"`
	Cluster string `json:"Cluster"`
	FQDN    string `json:"FQDN,omitempty"`
}

// ClusterInfo represents the cluster info returned by /api/v1/clusters/{name}.
type ClusterInfo struct {
	Cluster string `json:"cluster"`
	FQDN    string `json:"fqdn,omitempty"`
	Zone    string `json:"zone,omitempty"`
}

// ReserveRequest is the request body for reserving or updating an IP.
//
// The reserve endpoint takes no count: every call records exactly one address.
type ReserveRequest struct {
	Cluster string `json:"cluster"`
	// IP asks reserve for this address instead of any free one. clusterbook
	// answers 409 when it is taken and 404 when it is not in the pool.
	IP        string `json:"ip,omitempty"`
	CreateDNS bool   `json:"createDNS,omitempty"`
	Status    string `json:"status,omitempty"`
}

// DNSOutcome is the DNS half of a write, which clusterbook reports alongside
// the saved IP change ("ok", "failed" or "skipped").
type DNSOutcome struct {
	DNS      string `json:"dns,omitempty"`
	DNSError string `json:"dns_error,omitempty"`
}

// err returns a *DNSError when the DNS half failed.
func (o DNSOutcome) err(op string) error {
	if o.DNS != "failed" {
		return nil
	}
	return &DNSError{Op: op, Message: o.DNSError}
}

// DNSError reports that clusterbook saved an IP change but could not write or
// remove the cluster's DNS record. The HTTP status is still 200, so without it
// a failed record looks exactly like a written one.
type DNSError struct {
	Op      string
	Message string
}

func (e *DNSError) Error() string {
	return fmt.Sprintf("%s saved the IP change, but the DNS operation failed: %s", e.Op, e.Message)
}

// ReserveResponse is the response from the reserve endpoint.
type ReserveResponse struct {
	IP     string   `json:"ip"`
	IPs    []string `json:"ips"`
	Status string   `json:"status"`
	DNSOutcome
}

// ReleaseRequest is the request body for releasing an IP.
type ReleaseRequest struct {
	IP string `json:"ip"`
}

// ReserveIP reserves one address from the given network pool.
//
// When the address was recorded but its DNS record was not written, both the
// response and a *DNSError are returned.
func (c *Client) ReserveIP(ctx context.Context, networkKey string, req ReserveRequest) (*ReserveResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("cannot marshal reserve request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/networks/%s/reserve", c.baseURL, networkKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cannot create reserve request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("reserve request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("reserve request returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result ReserveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("cannot decode reserve response: %w", err)
	}
	if result.IP == "" && len(result.IPs) > 0 {
		result.IP = result.IPs[0]
	}
	if result.IP == "" {
		return nil, fmt.Errorf("reserve response carries no ip")
	}
	return &result, result.err("reserve")
}

// dnsOutcome reads the DNS verdict from a write response. A body without one
// (an empty 204, or a clusterbook older than v1.26.0) reports no failure.
func dnsOutcome(op string, body io.Reader) error {
	var o DNSOutcome
	_ = json.NewDecoder(body).Decode(&o) // no verdict decodes to none
	return o.err(op)
}

// GetIPs returns the IPs assigned to a cluster in the given network.
func (c *Client) GetIPs(ctx context.Context, networkKey string) ([]IPInfo, error) {
	url := fmt.Sprintf("%s/api/v1/networks/%s/ips", c.baseURL, networkKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create get IPs request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get IPs request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get IPs returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var ips []IPInfo
	if err := json.NewDecoder(resp.Body).Decode(&ips); err != nil {
		return nil, fmt.Errorf("cannot decode IPs response: %w", err)
	}
	return ips, nil
}

// GetClusterInfo returns the cluster info including FQDN and zone.
func (c *Client) GetClusterInfo(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	url := fmt.Sprintf("%s/api/v1/clusters/%s", c.baseURL, clusterName)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create get cluster info request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get cluster info request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get cluster info returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var info ClusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("cannot decode cluster info response: %w", err)
	}
	return &info, nil
}

// ReleaseIPs releases an IP assigned to a cluster in the given network. A
// *DNSError means the address was freed but its record was not removed.
func (c *Client) ReleaseIPs(ctx context.Context, networkKey string, req ReleaseRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("cannot marshal release request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/networks/%s/release", c.baseURL, networkKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot create release request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("release request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("release request returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return dnsOutcome("release", resp.Body)
}

// UpdateIP updates an existing IP assignment. clusterbook treats a status
// ending in ":DNS" as a request for DNS, so pass the bare status and CreateDNS.
// A *DNSError means the entry was saved but its record was not.
func (c *Client) UpdateIP(ctx context.Context, networkKey, ip string, req ReserveRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("cannot marshal update request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/networks/%s/ips/%s", c.baseURL, networkKey, ip)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot create update request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("update request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update request returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return dnsOutcome("update", resp.Body)
}
