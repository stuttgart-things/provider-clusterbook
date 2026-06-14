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

package v1alpha1

import (
	"reflect"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// IPReservationParameters are the configurable fields of an IPReservation.
type IPReservationParameters struct {
	// NetworkKey is the network pool key (e.g. "10.31.103").
	NetworkKey string `json:"networkKey"`

	// ClusterName is the cluster to assign IPs to.
	ClusterName string `json:"clusterName"`

	// Count is the number of IPs to reserve. Defaults to 1.
	// +kubebuilder:default=1
	// +optional
	Count int `json:"count,omitempty"`

	// IP is an optional explicit IP address (skip auto-reserve).
	// +optional
	IP string `json:"ip,omitempty"`

	// CreateDNS optionally creates a PDNS wildcard record for the reserved IP.
	// +optional
	CreateDNS bool `json:"createDNS,omitempty"`
}

// IPReservationObservation are the observable fields of an IPReservation.
type IPReservationObservation struct {
	// IPAddresses is the list of assigned IP addresses.
	// +optional
	IPAddresses []string `json:"ipAddresses,omitempty"`

	// Status is the assignment status (e.g. ASSIGNED, ASSIGNED:DNS).
	// +optional
	Status string `json:"status,omitempty"`

	// FQDN is the wildcard DNS name (e.g. *.mycluster.example.com).
	// Only set when createDNS is true and DNS is configured in clusterbook.
	// +optional
	FQDN string `json:"fqdn,omitempty"`

	// Zone is the DNS zone (e.g. example.com).
	// +optional
	Zone string `json:"zone,omitempty"`
}

// An IPReservationSpec defines the desired state of an IPReservation.
type IPReservationSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              IPReservationParameters `json:"forProvider"`
}

// An IPReservationStatus represents the observed state of an IPReservation.
type IPReservationStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 IPReservationObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="NETWORK",type="string",JSONPath=".spec.forProvider.networkKey"
// +kubebuilder:printcolumn:name="CLUSTER",type="string",JSONPath=".spec.forProvider.clusterName"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,clusterbook}
// An IPReservation reserves IP addresses from a clusterbook network pool.
type IPReservation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              IPReservationSpec   `json:"spec"`
	Status            IPReservationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// IPReservationList contains a list of IPReservation.
type IPReservationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPReservation `json:"items"`
}

// IPReservation type metadata.
var (
	IPReservationKind             = reflect.TypeOf(IPReservation{}).Name()
	IPReservationGroupKind        = schema.GroupKind{Group: Group, Kind: IPReservationKind}.String()
	IPReservationKindAPIVersion   = IPReservationKind + "." + SchemeGroupVersion.String()
	IPReservationGroupVersionKind = SchemeGroupVersion.WithKind(IPReservationKind)
)

func init() {
	SchemeBuilder.Register(&IPReservation{}, &IPReservationList{})
}
