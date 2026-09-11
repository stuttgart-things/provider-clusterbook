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

package ipreservation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	clusterbookclient "github.com/stuttgart-things/provider-clusterbook/internal/client"
)

// fakeClusterbook is an in-memory clusterbook for one network. It follows the
// REST semantics the controller depends on: reserve records one address per
// call (409 when an explicit ip is taken, 404 when it is not in the pool), a
// status ending in ":DNS" requests DNS, the ledger is saved before DNS is
// touched, and the DNS outcome is reported in the response body.
type fakeClusterbook struct {
	network string

	mu      sync.Mutex
	digits  []string
	entries map[string]*clusterbookclient.IPInfo
	failDNS bool
	calls   []string
}

func newFakeClusterbook(t *testing.T, network string, digits ...string) (*fakeClusterbook, *clusterbookclient.Client) {
	t.Helper()
	f := &fakeClusterbook{network: network, entries: map[string]*clusterbookclient.IPInfo{}}
	for _, d := range digits {
		f.digits = append(f.digits, d)
		f.entries[d] = &clusterbookclient.IPInfo{IP: network + "." + d, Digit: d}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/networks/{key}/ips", f.handleIPs)
	mux.HandleFunc("POST /api/v1/networks/{key}/reserve", f.handleReserve)
	mux.HandleFunc("POST /api/v1/networks/{key}/release", f.handleRelease)
	mux.HandleFunc("PUT /api/v1/networks/{key}/ips/{ip}", f.handleEdit)
	mux.HandleFunc("GET /api/v1/clusters/{name}", f.handleCluster)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := clusterbookclient.NewClient(srv.URL, nil)
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	return f, c
}

// set records an entry directly, bypassing the API.
func (f *fakeClusterbook) set(digit, cluster, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[digit].Cluster = cluster
	f.entries[digit].Status = status
}

func (f *fakeClusterbook) get(digit string) clusterbookclient.IPInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.entries[digit]
}

func (f *fakeClusterbook) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// dnsVerdict mimics clusterbook's dnsResult annotation.
func (f *fakeClusterbook) dnsVerdict(attempted bool, resp map[string]any) map[string]any {
	switch {
	case !attempted:
		resp["dns"] = "skipped"
	case f.failDNS:
		resp["dns"] = "failed"
		resp["dns_error"] = "pdns: connection refused"
	default:
		resp["dns"] = "ok"
	}
	return resp
}

func writeJSON(w http.ResponseWriter, body any) {
	if err := json.NewEncoder(w).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *fakeClusterbook) handleIPs(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]clusterbookclient.IPInfo, 0, len(f.digits))
	for _, d := range f.digits {
		out = append(out, *f.entries[d])
	}
	writeJSON(w, out)
}

func (f *fakeClusterbook) handleReserve(w http.ResponseWriter, r *http.Request) {
	var req clusterbookclient.ReserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("reserve ip=%s dns=%t", req.IP, req.CreateDNS))

	var digit string
	if req.IP != "" {
		digit = req.IP[strings.LastIndex(req.IP, ".")+1:]
		entry, ok := f.entries[digit]
		switch {
		case !ok:
			http.Error(w, `{"error":"not in the pool"}`, http.StatusNotFound)
			return
		case entry.Status != "":
			http.Error(w, `{"error":"not free"}`, http.StatusConflict)
			return
		}
	} else {
		for _, d := range f.digits {
			if f.entries[d].Status == "" {
				digit = d
				break
			}
		}
		if digit == "" {
			http.Error(w, `{"error":"no available IPs in network"}`, http.StatusConflict)
			return
		}
	}

	status := "ASSIGNED"
	if req.Status != "" {
		status = req.Status
	}
	createDNS := req.CreateDNS || strings.HasSuffix(status, ":DNS")
	if createDNS {
		status = strings.TrimSuffix(status, ":DNS") + ":DNS"
	}
	entry := f.entries[digit]
	entry.Status = status
	entry.Cluster = req.Cluster

	writeJSON(w, f.dnsVerdict(createDNS, map[string]any{
		"ip":      entry.IP,
		"ips":     []string{entry.IP},
		"status":  status,
		"cluster": req.Cluster,
	}))
}

func (f *fakeClusterbook) handleRelease(w http.ResponseWriter, r *http.Request) {
	var req clusterbookclient.ReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "release "+req.IP)

	entry, ok := f.entries[req.IP[strings.LastIndex(req.IP, ".")+1:]]
	if !ok {
		http.Error(w, `{"error":"ip not found"}`, http.StatusNotFound)
		return
	}
	hadDNS := strings.HasSuffix(entry.Status, ":DNS")
	entry.Status = ""
	entry.Cluster = ""

	writeJSON(w, f.dnsVerdict(hadDNS, map[string]any{"status": "ok"}))
}

func (f *fakeClusterbook) handleEdit(w http.ResponseWriter, r *http.Request) {
	var req clusterbookclient.ReserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if req.Cluster == "" || req.Status == "" {
		http.Error(w, `{"error":"cluster and status are required"}`, http.StatusBadRequest)
		return
	}

	ip := r.PathValue("ip")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("update %s status=%s dns=%t", ip, req.Status, req.CreateDNS))

	entry, ok := f.entries[ip[strings.LastIndex(ip, ".")+1:]]
	if !ok {
		http.Error(w, `{"error":"ip not found"}`, http.StatusNotFound)
		return
	}
	hadDNS := strings.HasSuffix(entry.Status, ":DNS")
	createDNS := req.CreateDNS || strings.HasSuffix(req.Status, ":DNS")

	entry.Status = strings.TrimSuffix(req.Status, ":DNS")
	if createDNS {
		entry.Status += ":DNS"
	}
	prevCluster := entry.Cluster
	entry.Cluster = req.Cluster

	attempted := createDNS || (hadDNS && (!createDNS || prevCluster != req.Cluster))
	writeJSON(w, f.dnsVerdict(attempted, map[string]any{"status": "ok"}))
}

func (f *fakeClusterbook) handleCluster(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.digits {
		if e := f.entries[d]; e.Cluster == name && strings.HasSuffix(e.Status, ":DNS") {
			writeJSON(w, clusterbookclient.ClusterInfo{
				Cluster: name, FQDN: "*." + name + ".example.com", Zone: "example.com",
			})
			return
		}
	}
	writeJSON(w, clusterbookclient.ClusterInfo{Cluster: name})
}
