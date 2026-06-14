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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv1 "github.com/crossplane/crossplane/apis/v2/core/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/stuttgart-things/provider-clusterbook/apis/ipreservation/v1alpha1"
	clusterbookclient "github.com/stuttgart-things/provider-clusterbook/internal/client"
)

// newTestCR creates a minimal IPReservation for testing.
func newTestCR(network, cluster string, count int, createDNS bool) *v1alpha1.IPReservation {
	return &v1alpha1.IPReservation{
		ObjectMeta: metav1.ObjectMeta{Name: "test-reservation"},
		Spec: v1alpha1.IPReservationSpec{
			ForProvider: v1alpha1.IPReservationParameters{
				NetworkKey:  network,
				ClusterName: cluster,
				Count:       count,
				CreateDNS:   createDNS,
			},
		},
	}
}

func TestObserve(t *testing.T) {
	t.Run("resource does not exist when no IPs assigned", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]clusterbookclient.IPInfo{ //nolint:errcheck
				{IP: "10.31.103.10", Status: "ASSIGNED", Cluster: "other"},
			})
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, false)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obs.ResourceExists {
			t.Error("resource should not exist")
		}
	})

	t.Run("resource exists and up to date without DNS", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode([]clusterbookclient.IPInfo{ //nolint:errcheck
				{IP: "10.31.103.10", Status: "ASSIGNED", Cluster: "mycluster"},
			})
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, false)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !obs.ResourceExists {
			t.Error("resource should exist")
		}
		if !obs.ResourceUpToDate {
			t.Error("resource should be up to date")
		}
		if cr.Status.AtProvider.Status != "ASSIGNED" {
			t.Errorf("status = %q, want ASSIGNED", cr.Status.AtProvider.Status)
		}
		if cr.Status.AtProvider.FQDN != "" {
			t.Errorf("FQDN should be empty without DNS, got %q", cr.Status.AtProvider.FQDN)
		}
	})

	t.Run("resource exists with DNS, populates FQDN and zone", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/networks/10.31.103/ips":
				json.NewEncoder(w).Encode([]clusterbookclient.IPInfo{ //nolint:errcheck
					{IP: "10.31.103.10", Status: "ASSIGNED:DNS", Cluster: "mycluster", FQDN: "*.mycluster.example.com"},
				})
			case "/api/v1/clusters/mycluster":
				json.NewEncoder(w).Encode(clusterbookclient.ClusterInfo{ //nolint:errcheck
					Cluster: "mycluster",
					FQDN:    "*.mycluster.example.com",
					Zone:    "example.com",
				})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !obs.ResourceExists {
			t.Error("resource should exist")
		}
		if !obs.ResourceUpToDate {
			t.Error("resource should be up to date")
		}
		if cr.Status.AtProvider.FQDN != "*.mycluster.example.com" {
			t.Errorf("FQDN = %q, want *.mycluster.example.com", cr.Status.AtProvider.FQDN)
		}
		if cr.Status.AtProvider.Zone != "example.com" {
			t.Errorf("Zone = %q, want example.com", cr.Status.AtProvider.Zone)
		}
		if cr.Status.AtProvider.Status != "ASSIGNED:DNS" {
			t.Errorf("Status = %q, want ASSIGNED:DNS", cr.Status.AtProvider.Status)
		}
	})

	t.Run("DNS requested but not yet active triggers update", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode([]clusterbookclient.IPInfo{ //nolint:errcheck
				{IP: "10.31.103.10", Status: "ASSIGNED", Cluster: "mycluster"},
			})
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !obs.ResourceExists {
			t.Error("resource should exist")
		}
		if obs.ResourceUpToDate {
			t.Error("resource should NOT be up to date (DNS requested but not active)")
		}
	})

	t.Run("count mismatch triggers update", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode([]clusterbookclient.IPInfo{ //nolint:errcheck
				{IP: "10.31.103.10", Status: "ASSIGNED", Cluster: "mycluster"},
			})
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, false)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obs.ResourceUpToDate {
			t.Error("resource should NOT be up to date (count mismatch)")
		}
	})

	t.Run("cluster info error is non-fatal for FQDN", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/networks/10.31.103/ips":
				json.NewEncoder(w).Encode([]clusterbookclient.IPInfo{ //nolint:errcheck
					{IP: "10.31.103.10", Status: "ASSIGNED:DNS", Cluster: "mycluster"},
				})
			case "/api/v1/clusters/mycluster":
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !obs.ResourceExists {
			t.Error("resource should exist")
		}
		// FQDN/Zone should be empty since cluster info call failed, but Observe should not error
		if cr.Status.AtProvider.FQDN != "" {
			t.Errorf("FQDN should be empty on cluster info error, got %q", cr.Status.AtProvider.FQDN)
		}
	})

	t.Run("not an IPReservation", func(t *testing.T) {
		e := &external{}
		_, err := e.Observe(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error for non-IPReservation")
		}
	})
}

func TestCreate(t *testing.T) {
	t.Run("reserves IPs", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req clusterbookclient.ReserveRequest
			json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
			if req.Cluster != "mycluster" || req.Count != 1 {
				t.Errorf("request = %+v", req)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(clusterbookclient.ReserveResponse{ //nolint:errcheck
				IPs:    []string{"10.31.103.10"},
				Status: "ASSIGNED",
			})
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, false)

		_, err := e.Create(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cr.Status.AtProvider.IPAddresses) != 1 || cr.Status.AtProvider.IPAddresses[0] != "10.31.103.10" {
			t.Errorf("IPs = %v", cr.Status.AtProvider.IPAddresses)
		}
	})

	t.Run("reserves IPs with DNS", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req clusterbookclient.ReserveRequest
			json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
			if !req.CreateDNS {
				t.Error("createDNS should be true")
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(clusterbookclient.ReserveResponse{ //nolint:errcheck
				IPs:    []string{"10.31.103.10"},
				Status: "ASSIGNED:DNS",
			})
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)

		_, err := e.Create(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cr.Status.AtProvider.Status != "ASSIGNED:DNS" {
			t.Errorf("Status = %q, want ASSIGNED:DNS", cr.Status.AtProvider.Status)
		}
	})

	t.Run("not an IPReservation", func(t *testing.T) {
		e := &external{}
		_, err := e.Create(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestUpdate(t *testing.T) {
	t.Run("updates IP with DNS flag", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut {
				t.Errorf("method = %s, want PUT", r.Method)
			}
			if r.URL.Path != "/api/v1/networks/10.31.103/ips/10.31.103.10" {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)
		cr.Status.AtProvider = v1alpha1.IPReservationObservation{
			IPAddresses: []string{"10.31.103.10"},
			Status:      "ASSIGNED",
		}

		_, err := e.Update(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("uses explicit spec IP", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/networks/10.31.103/ips/10.31.103.99" {
				t.Errorf("path = %s, want explicit IP", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)
		cr.Spec.ForProvider.IP = "10.31.103.99"
		cr.Status.AtProvider = v1alpha1.IPReservationObservation{
			IPAddresses: []string{"10.31.103.10"},
			Status:      "ASSIGNED",
		}

		_, err := e.Update(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("not an IPReservation", func(t *testing.T) {
		e := &external{}
		_, err := e.Update(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestDelete(t *testing.T) {
	t.Run("releases all IPs", func(t *testing.T) {
		var released []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req clusterbookclient.ReleaseRequest
			json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
			released = append(released, req.IP)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		c, _ := clusterbookclient.NewClient(srv.URL, nil)
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, false)
		cr.Status.AtProvider = v1alpha1.IPReservationObservation{
			IPAddresses: []string{"10.31.103.10", "10.31.103.11"},
		}

		_, err := e.Delete(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(released) != 2 {
			t.Errorf("released %d IPs, want 2", len(released))
		}
	})

	t.Run("not an IPReservation", func(t *testing.T) {
		e := &external{}
		_, err := e.Delete(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestDisconnect(t *testing.T) {
	e := &external{}
	if err := e.Disconnect(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Verify that external implements the managed.ExternalClient interface.
var _ managed.ExternalClient = &external{}

// Suppress unused import warnings.
var _ = xpv1.ConditionType("")
