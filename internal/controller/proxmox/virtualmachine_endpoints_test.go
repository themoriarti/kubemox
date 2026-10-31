package proxmox

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	proxmoxv1alpha1 "github.com/alperencelik/kubemox/api/proxmox/v1alpha1"
)

const endpointsTestName = "web"

func endpointsTestReconciler(objs ...client.Object) *VirtualMachineReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(proxmoxv1alpha1.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&proxmoxv1alpha1.VirtualMachine{}).Build()
	return &VirtualMachineReconciler{Client: cl, Scheme: scheme}
}

func TestReconcileExternalEndpoints(t *testing.T) {
	ctx := context.Background()
	vm := &proxmoxv1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: endpointsTestName, Namespace: sourceTestTenant, UID: "vm-uid",
			Annotations: map[string]string{ExternalServiceAnnotation: endpointsTestName}},
		Spec: proxmoxv1alpha1.VirtualMachineSpec{Name: endpointsTestName, NodeName: sourceTestNode},
		Status: proxmoxv1alpha1.VirtualMachineStatus{Networks: []proxmoxv1alpha1.VirtualMachineNetworkStatus{
			{Index: 0, Address: "10.0.0.5/24"}, {Index: 1}, {Index: 2, Address: "192.168.7.9/24"}}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: endpointsTestName, Namespace: sourceTestTenant},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "port-22", Port: 22, TargetPort: intstr.FromInt32(22)},
			{Name: "port-80", Port: 8080, TargetPort: intstr.FromInt32(80), Protocol: corev1.ProtocolTCP},
		}},
	}
	r := endpointsTestReconciler(vm, svc)
	if err := r.reconcileExternalEndpoints(ctx, vm); err != nil {
		t.Fatalf("reconcileExternalEndpoints: %v", err)
	}
	slice := &discoveryv1.EndpointSlice{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: sourceTestTenant, Name: endpointsTestName + "-vm"}, slice); err != nil {
		t.Fatalf("EndpointSlice not written: %v", err)
	}
	if slice.Labels[discoveryv1.LabelServiceName] != endpointsTestName || slice.AddressType != discoveryv1.AddressTypeIPv4 {
		t.Fatalf("labels/type: %v %s", slice.Labels, slice.AddressType)
	}
	if len(slice.Endpoints) != 1 || len(slice.Endpoints[0].Addresses) != 2 ||
		slice.Endpoints[0].Addresses[0] != "10.0.0.5" || slice.Endpoints[0].Addresses[1] != "192.168.7.9" {
		t.Fatalf("endpoints = %+v", slice.Endpoints)
	}
	if len(slice.Ports) != 2 || *slice.Ports[1].Port != 80 || *slice.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("ports = %+v", slice.Ports)
	}
	if len(slice.OwnerReferences) != 1 || slice.OwnerReferences[0].Name != endpointsTestName {
		t.Fatalf("owner = %+v", slice.OwnerReferences)
	}

	// The annotation goes: so does the slice.
	vm.Annotations = nil
	if err := r.reconcileExternalEndpoints(ctx, vm); err != nil {
		t.Fatalf("reconcileExternalEndpoints without annotation: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: sourceTestTenant, Name: endpointsTestName + "-vm"}, slice); err == nil {
		t.Fatal("EndpointSlice survived the annotation's removal")
	}

	// A Service that does not exist is reported, not an error.
	vm.Annotations = map[string]string{ExternalServiceAnnotation: "absent"}
	if err := r.reconcileExternalEndpoints(ctx, vm); err != nil {
		t.Fatalf("missing Service: %v", err)
	}
}

func TestMarkStartedOnce(t *testing.T) {
	ctx := context.Background()
	vm := &proxmoxv1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "once", Namespace: sourceTestTenant},
		Spec:       proxmoxv1alpha1.VirtualMachineSpec{Name: "once", NodeName: sourceTestNode, StartOnce: true},
	}
	r := endpointsTestReconciler(vm)
	if err := r.markStartedOnce(ctx, vm); err != nil {
		t.Fatalf("markStartedOnce: %v", err)
	}
	got := &proxmoxv1alpha1.VirtualMachine{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(vm), got); err != nil || !got.Status.StartedOnce {
		t.Fatalf("StartedOnce not recorded: %v %v", got.Status.StartedOnce, err)
	}
	plain := &proxmoxv1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "always", Namespace: sourceTestTenant},
		Spec:       proxmoxv1alpha1.VirtualMachineSpec{Name: "always", NodeName: sourceTestNode},
	}
	r = endpointsTestReconciler(plain)
	if err := r.markStartedOnce(ctx, plain); err != nil || plain.Status.StartedOnce {
		t.Fatalf("a machine without startOnce was marked: %v %v", plain.Status.StartedOnce, err)
	}
}
