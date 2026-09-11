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
	p := cr.Spec.ForProvider

	entries, err := e.client.GetIPs(ctx, p.NetworkKey)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetIPs)
	}

	held := heldBy(entries, p.ClusterName)
	if len(held) == 0 {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	marked := dnsMarked(held)

	// Fetch FQDN and zone from cluster info when DNS is active
	var fqdn, zone string
	if marked > 0 {
		if info, err := e.client.GetClusterInfo(ctx, p.ClusterName); err == nil {
			fqdn = info.FQDN
			zone = info.Zone
		}
	}

	cr.Status.AtProvider = v1alpha1.IPReservationObservation{
		IPAddresses: addresses(held),
		Status:      held[primary(held, p)].Status,
		FQDN:        fqdn,
		Zone:        zone,
	}
	cr.SetConditions(xpv2.Available())

	// clusterbook saves the ":DNS" marker before it touches DNS, so after a
	// failed DNS write the marker is there without the record. When the last
	// reconcile failed, let Update re-assert the record once; both DNS
	// providers are idempotent.
	retryDNS := p.CreateDNS && cr.GetCondition(xpv2.TypeSynced).Reason == xpv2.ReasonReconcileError

	// clusterbook keeps one record per cluster, so exactly one address may
	// carry the marker with createDNS and none without.
	wantMarked := 0
	if p.CreateDNS {
		wantMarked = 1
	}
	countMatch := len(held) == wantedCount(p)
	dnsMatch := marked == wantMarked

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: countMatch && dnsMatch && !retryDNS,
	}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.IPReservation)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotIPReservation)
	}
	p := cr.Spec.ForProvider

	entries, err := e.client.GetIPs(ctx, p.NetworkKey)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errGetIPs)
	}

	held, err := e.reserveMissing(ctx, p, entries, p.CreateDNS)
	if err != nil {
		// Record nothing: a partial status would read as complete. Observe
		// finds what was reserved so far and Update adds the rest.
		return managed.ExternalCreation{}, errors.Wrap(err, errReserveIPs)
	}

	cr.Status.AtProvider = v1alpha1.IPReservationObservation{
		IPAddresses: addresses(held),
		Status:      held[primary(held, p)].Status,
	}

	return managed.ExternalCreation{}, nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.IPReservation)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotIPReservation)
	}
	p := cr.Spec.ForProvider

	entries, err := e.client.GetIPs(ctx, p.NetworkKey)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errGetIPs)
	}

	// DNS is left to syncDNS, which picks the address that carries the record.
	held, err := e.reserveMissing(ctx, p, entries, false)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errReserveIPs)
	}

	held, err = e.releaseSurplus(ctx, p, held)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errReleaseIPs)
	}

	if err := e.syncDNS(ctx, p, held); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateIP)
	}

	return managed.ExternalUpdate{}, nil
}

// reserveMissing reserves the addresses the cluster still lacks and returns
// everything it holds afterwards. reserve takes no count, so it is called once
// per address. Starting from what the cluster already holds makes a retry
// after a partial failure add only the rest instead of leaking a second set.
// With createDNS the first new address asks for the record, unless one of the
// held addresses already carries it.
func (e *external) reserveMissing(ctx context.Context, p v1alpha1.IPReservationParameters, entries []clusterbookclient.IPInfo, createDNS bool) ([]clusterbookclient.IPInfo, error) {
	held := heldBy(entries, p.ClusterName)

	need := wantedCount(p) - len(held)
	if need <= 0 {
		return held, nil
	}
	// Fail before reserving anything rather than leave a partial set behind.
	if free := countFree(entries); free < need {
		return held, errors.Errorf("not enough free IPs in network %s: need %d, have %d", p.NetworkKey, need, free)
	}

	explicit := p.IP
	if explicit != "" && holds(held, fullIP(p.NetworkKey, explicit)) {
		explicit = ""
	}
	createDNS = createDNS && dnsMarked(held) == 0

	for i := 0; i < need; i++ {
		req := clusterbookclient.ReserveRequest{Cluster: p.ClusterName}
		if i == 0 {
			req.IP = explicit
			req.CreateDNS = createDNS
		}

		resp, err := e.client.ReserveIP(ctx, p.NetworkKey, req)
		if err != nil {
			if resp != nil {
				return held, errors.Wrapf(err, "reserved IP %s", resp.IP)
			}
			return held, err
		}
		held = append(held, clusterbookclient.IPInfo{IP: resp.IP, Status: resp.Status, Cluster: p.ClusterName})
	}

	return held, nil
}

// releaseSurplus releases the addresses beyond count and returns the rest.
// The primary address and the explicit spec IP are kept first.
func (e *external) releaseSurplus(ctx context.Context, p v1alpha1.IPReservationParameters, held []clusterbookclient.IPInfo) ([]clusterbookclient.IPInfo, error) {
	want := wantedCount(p)
	if len(held) <= want {
		return held, nil
	}

	// Order by what to keep: primary, explicit spec IP, then server order.
	pi := primary(held, p)
	ordered := []clusterbookclient.IPInfo{held[pi]}
	var rest []clusterbookclient.IPInfo
	for i, entry := range held {
		switch {
		case i == pi:
		case p.IP != "" && entry.IP == fullIP(p.NetworkKey, p.IP):
			ordered = append(ordered, entry)
		default:
			rest = append(rest, entry)
		}
	}
	ordered = append(ordered, rest...)

	for _, entry := range ordered[want:] {
		if err := e.client.ReleaseIPs(ctx, p.NetworkKey, clusterbookclient.ReleaseRequest{IP: entry.IP}); err != nil {
			return held, errors.Wrapf(err, "releasing surplus IP %s", entry.IP)
		}
	}

	return ordered[:want], nil
}

// syncDNS brings the ":DNS" markers in line with the spec. clusterbook keeps
// one record per cluster, so only the primary address may carry it.
// Withdrawing a marker deletes the cluster's record, so stray markers are
// removed first and the primary is asserted last. The primary is always
// re-asserted: a failed DNS write leaves the marker without the record.
func (e *external) syncDNS(ctx context.Context, p v1alpha1.IPReservationParameters, held []clusterbookclient.IPInfo) error {
	if len(held) == 0 {
		return nil
	}
	pi := primary(held, p)

	for i, entry := range held {
		if (i == pi && p.CreateDNS) || !hasDNS(entry.Status) {
			continue
		}
		if err := e.updateEntry(ctx, p, entry, false); err != nil {
			return err
		}
	}

	if p.CreateDNS {
		return e.updateEntry(ctx, p, held[pi], true)
	}
	return nil
}

// updateEntry rewrites one entry with its status stripped of ":DNS" and the
// DNS flag set explicitly. Sending the marked status would switch DNS on
// regardless of createDNS, so it could never be turned off.
func (e *external) updateEntry(ctx context.Context, p v1alpha1.IPReservationParameters, entry clusterbookclient.IPInfo, createDNS bool) error {
	status := strings.TrimSuffix(entry.Status, dnsMarker)
	if status == "" {
		status = defaultStatus
	}
	req := clusterbookclient.ReserveRequest{
		Cluster:   p.ClusterName,
		Status:    status,
		CreateDNS: createDNS,
	}
	return errors.Wrapf(e.client.UpdateIP(ctx, p.NetworkKey, entry.IP, req), "updating IP %s", entry.IP)
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

const (
	// dnsMarker is the status suffix clusterbook uses for DNS-backed entries.
	dnsMarker = ":DNS"
	// defaultStatus is what clusterbook records when a write names no status.
	defaultStatus = "ASSIGNED"
)

func hasDNS(status string) bool { return strings.HasSuffix(status, dnsMarker) }

// wantedCount is spec.count, which defaults to 1.
func wantedCount(p v1alpha1.IPReservationParameters) int {
	if p.Count < 1 {
		return 1
	}
	return p.Count
}

// heldBy returns the entries assigned to cluster, in server order. Only an
// empty status is free in clusterbook, so an entry without one is not held.
func heldBy(entries []clusterbookclient.IPInfo, cluster string) []clusterbookclient.IPInfo {
	var held []clusterbookclient.IPInfo
	for _, entry := range entries {
		if entry.Cluster == cluster && entry.Status != "" {
			held = append(held, entry)
		}
	}
	return held
}

// countFree returns how many entries clusterbook would hand out.
func countFree(entries []clusterbookclient.IPInfo) int {
	free := 0
	for _, entry := range entries {
		if entry.Status == "" {
			free++
		}
	}
	return free
}

// dnsMarked counts the entries carrying the ":DNS" marker.
func dnsMarked(entries []clusterbookclient.IPInfo) int {
	n := 0
	for _, entry := range entries {
		if hasDNS(entry.Status) {
			n++
		}
	}
	return n
}

func holds(entries []clusterbookclient.IPInfo, ip string) bool {
	for _, entry := range entries {
		if entry.IP == ip {
			return true
		}
	}
	return false
}

func addresses(entries []clusterbookclient.IPInfo) []string {
	ips := make([]string, len(entries))
	for i, entry := range entries {
		ips[i] = entry.IP
	}
	return ips
}

// primary returns the index of the entry that carries, or should carry, the
// cluster's DNS record: the one already marked, else the explicit spec IP,
// else the first. entries must not be empty.
func primary(entries []clusterbookclient.IPInfo, p v1alpha1.IPReservationParameters) int {
	for i, entry := range entries {
		if hasDNS(entry.Status) {
			return i
		}
	}
	if p.IP != "" {
		for i, entry := range entries {
			if entry.IP == fullIP(p.NetworkKey, p.IP) {
				return i
			}
		}
	}
	return 0
}

// fullIP expands a host part ("50") to the address clusterbook lists
// ("10.31.103.50"); a full address is returned as is.
func fullIP(networkKey, ip string) string {
	if strings.Contains(ip, ".") {
		return ip
	}
	return networkKey + "." + ip
}
