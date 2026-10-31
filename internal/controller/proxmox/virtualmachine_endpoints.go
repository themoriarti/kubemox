package proxmox

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	proxmoxv1alpha1 "github.com/alperencelik/kubemox/api/proxmox/v1alpha1"
)

// A machine on Proxmox is no pod, so a Service cannot select it. A chart
// that publishes the machine renders the Service without a selector and
// names it on the VirtualMachine; the operator keeps an EndpointSlice with
// the machine's addresses behind it, the way the endpoint controller does
// for pods.

const (
	// ExternalServiceAnnotation names the Service of this namespace whose
	// endpoints are the machine's addresses.
	ExternalServiceAnnotation = "networking.cozystack.io/service"
	// endpointSliceVMLabel marks the slices this operator writes for a machine.
	endpointSliceVMLabel = "proxmox.alperen.cloud/virtualmachine"
	endpointSliceManager = "kubemox"
)

// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

// reconcileExternalEndpoints writes the EndpointSlice for the Service the
// annotation names from status.networks, and removes the slices of a
// Service the annotation no longer names.
func (r *VirtualMachineReconciler) reconcileExternalEndpoints(ctx context.Context, vm *proxmoxv1alpha1.VirtualMachine) error {
	svcName := vm.GetAnnotations()[ExternalServiceAnnotation]

	var slices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slices, client.InNamespace(vm.Namespace),
		client.MatchingLabels{endpointSliceVMLabel: vm.Name}); err != nil {
		return fmt.Errorf("listing EndpointSlices of %s: %w", vm.Name, err)
	}
	for i := range slices.Items {
		if svcName == "" || slices.Items[i].Labels[discoveryv1.LabelServiceName] != svcName {
			if err := r.Delete(ctx, &slices.Items[i]); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("removing EndpointSlice %s: %w", slices.Items[i].Name, err)
			}
		}
	}
	if svcName == "" {
		return nil
	}

	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: vm.Namespace, Name: svcName}, svc); err != nil {
		if kerrors.IsNotFound(err) {
			r.networkEvent(vm, "Warning", "ServiceNotFound",
				"Service %s named by %s does not exist; the machine is not published", svcName, ExternalServiceAnnotation)
			return nil
		}
		return fmt.Errorf("reading Service %s: %w", svcName, err)
	}

	addresses := make([]string, 0, len(vm.Status.Networks))
	for _, n := range vm.Status.Networks {
		if ip, _, found := strings.Cut(n.Address, "/"); found && ip != "" {
			addresses = append(addresses, ip)
		}
	}
	if len(addresses) == 0 {
		return nil
	}

	ports := make([]discoveryv1.EndpointPort, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		port := p.TargetPort.IntVal
		if port == 0 {
			port = p.Port
		}
		proto := p.Protocol
		if proto == "" {
			proto = corev1.ProtocolTCP
		}
		ports = append(ports, discoveryv1.EndpointPort{
			Name: ptr.To(p.Name), Port: ptr.To(port), Protocol: ptr.To(proto)})
	}

	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
		Name: svcName + "-vm", Namespace: vm.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, slice, func() error {
		if slice.Labels == nil {
			slice.Labels = map[string]string{}
		}
		slice.Labels[discoveryv1.LabelServiceName] = svcName
		slice.Labels[discoveryv1.LabelManagedBy] = endpointSliceManager
		slice.Labels[endpointSliceVMLabel] = vm.Name
		slice.AddressType = discoveryv1.AddressTypeIPv4
		slice.Ports = ports
		slice.Endpoints = []discoveryv1.Endpoint{{
			Addresses:  addresses,
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
		}}
		return controllerutil.SetControllerReference(vm, slice, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("writing EndpointSlice for Service %s: %w", svcName, err)
	}
	return nil
}
