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
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/stuttgart-things/provider-clusterbook/apis/ipreservation/v1alpha1"
	apisv1alpha1 "github.com/stuttgart-things/provider-clusterbook/apis/v1alpha1"
	clusterbookclient "github.com/stuttgart-things/provider-clusterbook/internal/client"
)

const (
	errNotIPReservation = "managed resource is not an IPReservation custom resource"
	errTrackPCUsage     = "cannot track ProviderConfig usage"
	errGetPC            = "cannot get ProviderConfig"
	errGetCPC           = "cannot get ClusterProviderConfig"
	errReserveIPs       = "cannot reserve IPs from clusterbook"
	errGetIPs           = "cannot get IPs from clusterbook"
	errReleaseIPs       = "cannot release IPs from clusterbook"
	errUpdateIP         = "cannot update IP in clusterbook"
)

// SetupGated adds a controller that reconciles IPReservation managed resources with safe-start support.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			panic(errors.Wrap(err, "cannot setup IPReservation controller"))
		}
	}, v1alpha1.IPReservationGroupVersionKind)
	return nil
}

// Setup adds a controller that reconciles IPReservation managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.IPReservationGroupKind)

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnector(&connector{
			kube:  mgr.GetClient(),
			usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ClusterProviderConfigUsage{}),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))), //nolint:staticcheck // crossplane event.NewAPIRecorder requires the legacy record.EventRecorder
	}

	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		opts = append(opts, managed.WithManagementPolicies())
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		opts = append(opts, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
	}

	if o.MetricOptions != nil && o.MetricOptions.MRStateMetrics != nil {
		stateMetricsRecorder := statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.IPReservationList{}, o.MetricOptions.PollStateMetricInterval,
		)
		if err := mgr.Add(stateMetricsRecorder); err != nil {
			return errors.Wrap(err, "cannot register MR state metrics recorder for kind v1alpha1.IPReservationList")
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.IPReservationGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.IPReservation{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type connector struct {
	kube  client.Client
	usage *resource.ProviderConfigUsageTracker
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.IPReservation)
	if !ok {
		return nil, errors.New(errNotIPReservation)
	}

	if err := c.usage.Track(ctx, cr); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	spec, err := c.resolveProviderConfigSpec(ctx, cr)
	if err != nil {
		return nil, err
	}

	cbClient, err := clusterbookclient.NewClient(spec.URL, &clusterbookclient.TLSOptions{
		InsecureSkipVerify: spec.InsecureSkipTLSVerify,
		CustomCA:           spec.CustomCA,
	})
	if err != nil {
		return nil, errors.Wrap(err, "cannot create clusterbook client")
	}

	return &external{
		kube:   c.kube,
		client: cbClient,
	}, nil
}

func (c *connector) resolveProviderConfigSpec(ctx context.Context, cr *v1alpha1.IPReservation) (*apisv1alpha1.ProviderConfigSpec, error) {
	ref := cr.GetProviderConfigReference()
	if ref == nil {
		return nil, errors.New("providerConfigRef is not set")
	}

	switch ref.Kind {
	case "ProviderConfig":
		pc := &apisv1alpha1.ProviderConfig{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name}, pc); err != nil {
			return nil, errors.Wrap(err, errGetPC)
		}
		return &pc.Spec, nil
	case "ClusterProviderConfig":
		cpc := &apisv1alpha1.ClusterProviderConfig{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name}, cpc); err != nil {
			return nil, errors.Wrap(err, errGetCPC)
		}
		return &cpc.Spec, nil
	default:
		return nil, errors.Errorf("unsupported provider config kind: %s", ref.Kind)
	}
}

type external struct {
	kube   client.Client
	client *clusterbookclient.Client
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.IPReservation)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotIPReservation)
	}

	ips, err := e.client.GetIPs(ctx, cr.Spec.ForProvider.NetworkKey)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetIPs)
	}

	// Find IPs assigned to this cluster
	var assignedIPs []string
	var status string
	for _, ip := range ips {
		if ip.Cluster == cr.Spec.ForProvider.ClusterName {
			assignedIPs = append(assignedIPs, ip.IP)
			status = ip.Status
		}
	}

	if len(assignedIPs) == 0 {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// Fetch FQDN and zone from cluster info when DNS is active
	var fqdn, zone string
	if strings.Contains(status, "DNS") {
		if info, err := e.client.GetClusterInfo(ctx, cr.Spec.ForProvider.ClusterName); err == nil {
			fqdn = info.FQDN
			zone = info.Zone
		}
	}

	cr.Status.AtProvider = v1alpha1.IPReservationObservation{
		IPAddresses: assignedIPs,
		Status:      status,
		FQDN:        fqdn,
		Zone:        zone,
	}
	cr.SetConditions(xpv2.Available())

	// Check if the reservation matches desired state
	countMatch := len(assignedIPs) == cr.Spec.ForProvider.Count
	dnsMatch := !cr.Spec.ForProvider.CreateDNS || strings.Contains(status, "DNS")
	upToDate := countMatch && dnsMatch

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: upToDate,
	}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.IPReservation)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotIPReservation)
	}

	req := clusterbookclient.ReserveRequest{
		Cluster:   cr.Spec.ForProvider.ClusterName,
		Count:     cr.Spec.ForProvider.Count,
		IP:        cr.Spec.ForProvider.IP,
		CreateDNS: cr.Spec.ForProvider.CreateDNS,
	}

	resp, err := e.client.ReserveIPs(ctx, cr.Spec.ForProvider.NetworkKey, req)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errReserveIPs)
	}

	cr.Status.AtProvider = v1alpha1.IPReservationObservation{
		IPAddresses: resp.IPs,
		Status:      resp.Status,
	}

	return managed.ExternalCreation{}, nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.IPReservation)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotIPReservation)
	}

	// Determine IP to update: explicit spec IP or first assigned IP from status
	ip := cr.Spec.ForProvider.IP
	if ip == "" && len(cr.Status.AtProvider.IPAddresses) > 0 {
		ip = cr.Status.AtProvider.IPAddresses[0]
	}

	if ip != "" {
		req := clusterbookclient.ReserveRequest{
			Cluster:   cr.Spec.ForProvider.ClusterName,
			CreateDNS: cr.Spec.ForProvider.CreateDNS,
			Status:    cr.Status.AtProvider.Status,
		}
		if err := e.client.UpdateIP(ctx, cr.Spec.ForProvider.NetworkKey, ip, req); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateIP)
		}
	}

	return managed.ExternalUpdate{}, nil
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.IPReservation)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotIPReservation)
	}

	for _, ip := range cr.Status.AtProvider.IPAddresses {
		req := clusterbookclient.ReleaseRequest{IP: ip}
		if err := e.client.ReleaseIPs(ctx, cr.Spec.ForProvider.NetworkKey, req); err != nil {
			return managed.ExternalDelete{}, errors.Wrap(err, errReleaseIPs)
		}
	}

	return managed.ExternalDelete{}, nil
}

func (e *external) Disconnect(_ context.Context) error {
	return nil
}
