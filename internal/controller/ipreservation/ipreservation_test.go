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
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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

	t.Run("DNS active but not wanted triggers update", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1")
		f.set("1", "mycluster", "ASSIGNED:DNS")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, false)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !obs.ResourceExists || obs.ResourceUpToDate {
			t.Errorf("observation = %+v, want existing but not up to date", obs)
		}
	})

	t.Run("more than one DNS marker triggers update", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2")
		f.set("1", "mycluster", "ASSIGNED:DNS")
		f.set("2", "mycluster", "ASSIGNED:DNS")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, true)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !obs.ResourceExists || obs.ResourceUpToDate {
			t.Errorf("observation = %+v, want existing but not up to date", obs)
		}
	})

	t.Run("failed last reconcile re-asserts DNS", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1")
		f.set("1", "mycluster", "ASSIGNED:DNS")
		e := &external{client: c}

		cr := newTestCR("10.31.103", "mycluster", 1, true)
		cr.SetConditions(xpv1.ReconcileError(errors.New("dns operation failed")))
		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obs.ResourceUpToDate {
			t.Error("resource should NOT be up to date after a failed reconcile with createDNS")
		}

		cr.SetConditions(xpv1.ReconcileSuccess())
		if obs, _ := e.Observe(context.Background(), cr); !obs.ResourceUpToDate {
			t.Error("resource should be up to date after a successful reconcile")
		}

		cr = newTestCR("10.31.103", "mycluster", 1, false)
		f.set("1", "mycluster", "ASSIGNED")
		cr.SetConditions(xpv1.ReconcileError(errors.New("boom")))
		if obs, _ := e.Observe(context.Background(), cr); !obs.ResourceUpToDate {
			t.Error("a failed reconcile without createDNS must not force an update")
		}
	})

	t.Run("entry naming the cluster without a status is not held", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1")
		f.set("1", "mycluster", "")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, false)

		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obs.ResourceExists {
			t.Error("a free entry must not count as held")
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
	t.Run("reserves one address per count, DNS on the first only", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2", "3", "4", "5")
		f.set("1", "other", "ASSIGNED")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 3, true)

		if _, err := e.Create(context.Background(), cr); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantCalls := []string{"reserve ip= dns=true", "reserve ip= dns=false", "reserve ip= dns=false"}
		if got := f.recorded(); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
		wantIPs := []string{"10.31.103.2", "10.31.103.3", "10.31.103.4"}
		if !reflect.DeepEqual(cr.Status.AtProvider.IPAddresses, wantIPs) {
			t.Errorf("IPs = %v, want %v", cr.Status.AtProvider.IPAddresses, wantIPs)
		}
		if cr.Status.AtProvider.Status != "ASSIGNED:DNS" {
			t.Errorf("Status = %q, want ASSIGNED:DNS", cr.Status.AtProvider.Status)
		}
	})

	t.Run("reserves the explicit IP first", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2", "50")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, false)
		cr.Spec.ForProvider.IP = "10.31.103.50"

		if _, err := e.Create(context.Background(), cr); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantCalls := []string{"reserve ip=10.31.103.50 dns=false", "reserve ip= dns=false"}
		if got := f.recorded(); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
		if got := f.get("50"); got.Cluster != "mycluster" {
			t.Errorf("explicit IP held by %q, want mycluster", got.Cluster)
		}
	})

	t.Run("explicit IP held by another cluster fails", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "50")
		f.set("50", "other", "ASSIGNED")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, false)
		cr.Spec.ForProvider.IP = "10.31.103.50"

		if _, err := e.Create(context.Background(), cr); err == nil {
			t.Fatal("expected error for a taken explicit IP")
		}
		if got := f.get("50"); got.Cluster != "other" {
			t.Errorf("explicit IP taken over: held by %q", got.Cluster)
		}
		if got := f.get("1"); got.Status != "" {
			t.Errorf("fell back to another address: %+v", got)
		}
	})

	t.Run("too small a pool fails before reserving", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2")
		f.set("1", "other", "ASSIGNED")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, false)

		if _, err := e.Create(context.Background(), cr); err == nil {
			t.Fatal("expected error for a too small pool")
		}
		if got := f.recorded(); len(got) != 0 {
			t.Errorf("reserved despite the pool being too small: %v", got)
		}
	})

	t.Run("reported DNS failure is an error and records no status", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2")
		f.failDNS = true
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)

		_, err := e.Create(context.Background(), cr)
		var dnsErr *clusterbookclient.DNSError
		if !errors.As(err, &dnsErr) {
			t.Fatalf("err = %v, want a DNSError", err)
		}
		if len(cr.Status.AtProvider.IPAddresses) != 0 {
			t.Errorf("recorded status despite the failure: %v", cr.Status.AtProvider.IPAddresses)
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
	t.Run("reserves the remainder", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2", "3")
		f.set("1", "mycluster", "ASSIGNED")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 3, false)

		if _, err := e.Update(context.Background(), cr); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantCalls := []string{"reserve ip= dns=false", "reserve ip= dns=false"}
		if got := f.recorded(); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
	})

	t.Run("releases the surplus, keeping the DNS primary and the explicit IP", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2", "3")
		f.set("1", "mycluster", "ASSIGNED")
		f.set("2", "mycluster", "ASSIGNED:DNS")
		f.set("3", "mycluster", "ASSIGNED")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, true)
		cr.Spec.ForProvider.IP = "3"

		if _, err := e.Update(context.Background(), cr); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantCalls := []string{"release 10.31.103.1", "update 10.31.103.2 status=ASSIGNED dns=true"}
		if got := f.recorded(); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
	})

	t.Run("turns DNS off by sending the bare status", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1")
		f.set("1", "mycluster", "ASSIGNED:DNS")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, false)

		if _, err := e.Update(context.Background(), cr); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantCalls := []string{"update 10.31.103.1 status=ASSIGNED dns=false"}
		if got := f.recorded(); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
		if got := f.get("1").Status; got != "ASSIGNED" {
			t.Errorf("status = %q, want ASSIGNED", got)
		}
	})

	t.Run("withdraws stray DNS markers before asserting the primary", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2")
		f.set("1", "mycluster", "ASSIGNED:DNS")
		f.set("2", "mycluster", "ASSIGNED:DNS")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, true)

		if _, err := e.Update(context.Background(), cr); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// clusterbook keeps one record per cluster: withdrawing .2 deletes it,
		// so .1 must be asserted afterwards.
		wantCalls := []string{
			"update 10.31.103.2 status=ASSIGNED dns=false",
			"update 10.31.103.1 status=ASSIGNED dns=true",
		}
		if got := f.recorded(); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
	})

	t.Run("reported DNS failure is an error", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1")
		f.set("1", "mycluster", "ASSIGNED")
		f.failDNS = true
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)

		_, err := e.Update(context.Background(), cr)
		var dnsErr *clusterbookclient.DNSError
		if !errors.As(err, &dnsErr) {
			t.Fatalf("err = %v, want a DNSError", err)
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

// TestReconcileConverges drives Observe/Create/Update the way the managed
// reconciler does and checks that the resource settles.
func TestReconcileConverges(t *testing.T) {
	observe := func(t *testing.T, e *external, cr *v1alpha1.IPReservation) managed.ExternalObservation {
		t.Helper()
		obs, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("observe: %v", err)
		}
		return obs
	}

	t.Run("count > 1 with DNS", func(t *testing.T) {
		_, c := newFakeClusterbook(t, "10.31.103", "1", "2", "3")
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 2, true)

		if obs := observe(t, e, cr); obs.ResourceExists {
			t.Fatal("resource should not exist yet")
		}
		if _, err := e.Create(context.Background(), cr); err != nil {
			t.Fatalf("create: %v", err)
		}
		cr.SetConditions(xpv1.ReconcileSuccess())

		obs := observe(t, e, cr)
		if !obs.ResourceExists || !obs.ResourceUpToDate {
			t.Fatalf("observation = %+v, want existing and up to date", obs)
		}
		if len(cr.Status.AtProvider.IPAddresses) != 2 {
			t.Errorf("IPs = %v, want 2", cr.Status.AtProvider.IPAddresses)
		}
		if cr.Status.AtProvider.FQDN != "*.mycluster.example.com" {
			t.Errorf("FQDN = %q", cr.Status.AtProvider.FQDN)
		}
	})

	t.Run("DNS failure during create is retried through update", func(t *testing.T) {
		f, c := newFakeClusterbook(t, "10.31.103", "1", "2")
		f.failDNS = true
		e := &external{client: c}
		cr := newTestCR("10.31.103", "mycluster", 1, true)

		_, err := e.Create(context.Background(), cr)
		if err == nil {
			t.Fatal("expected create to fail on DNS")
		}
		cr.SetConditions(xpv1.ReconcileError(err))

		// The ledger carries ":DNS" although the record was never written.
		obs := observe(t, e, cr)
		if !obs.ResourceExists || obs.ResourceUpToDate {
			t.Fatalf("observation = %+v, want existing but not up to date", obs)
		}

		f.failDNS = false
		if _, err := e.Update(context.Background(), cr); err != nil {
			t.Fatalf("update: %v", err)
		}
		cr.SetConditions(xpv1.ReconcileSuccess())

		if obs := observe(t, e, cr); !obs.ResourceUpToDate {
			t.Fatal("resource should be up to date once DNS went through")
		}
		wantCalls := []string{"reserve ip= dns=true", "update 10.31.103.1 status=ASSIGNED dns=true"}
		if got := f.recorded(); !reflect.DeepEqual(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
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
