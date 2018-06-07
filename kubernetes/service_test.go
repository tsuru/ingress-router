// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kubernetes

import (
	"reflect"
	"testing"

	"github.com/tsuru/kubernetes-router/router"
	tsuruv1 "github.com/tsuru/tsuru/provision/kubernetes/pkg/apis/tsuru/v1"
	faketsuru "github.com/tsuru/tsuru/provision/kubernetes/pkg/client/clientset/versioned/fake"
	"k8s.io/api/core/v1"
	v1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeapiextensions "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAddresses(t *testing.T) {
	svc1 := v1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:      "test",
		Namespace: "default",
		Labels:    map[string]string{appLabel: "test", appPoolLabel: "pool"},
	},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{Protocol: "TCP", Port: int32(8899), TargetPort: intstr.FromInt(8899), NodePort: 9090}},
		},
	}
	node := v1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{poolLabel: "pool"},
	},
		Status: v1.NodeStatus{Addresses: []v1.NodeAddress{{Type: v1.NodeInternalIP, Address: "192.168.10.1"}}},
	}
	svc := createFakeService()
	err := svc.Create("test", router.Opts{})
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	_, err = svc.Client.CoreV1().Services(svc.Namespace).Create(&svc1)
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	_, err = svc.Client.CoreV1().Nodes().Create(&node)
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}

	expected := []string{"http://192.168.10.1:9090"}
	addresses, err := svc.Addresses("test")
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	if !reflect.DeepEqual(addresses, expected) {
		t.Errorf("Expected %v. Got %v.", expected, addresses)
	}
}

func TestGetWebService(t *testing.T) {
	headlessSvc := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-headless",
			Namespace: "default",
			Labels:    map[string]string{appLabel: "test", headlessServiceLabel: "true"},
		},
		Spec: v1.ServiceSpec{
			Selector: map[string]string{"name": "test"},
		},
	}
	svc := BaseService{
		Namespace:        "default",
		Client:           fake.NewSimpleClientset(),
		TsuruClient:      faketsuru.NewSimpleClientset(),
		ExtensionsClient: fakeapiextensions.NewSimpleClientset(),
	}
	_, err := svc.Client.CoreV1().Services(svc.Namespace).Create(&headlessSvc)
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	webService, err := svc.getWebService("test")
	expectedErr := ErrNoService{App: "test"}
	if err != expectedErr {
		t.Errorf("Expected err to be %v. Got %v. Got service: %v", expectedErr, err, webService)
	}
	svc1 := v1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:      "test-single",
		Namespace: "default",
		Labels:    map[string]string{appLabel: "test"},
	},
		Spec: v1.ServiceSpec{
			Selector: map[string]string{"name": "test-single"},
			Ports:    []v1.ServicePort{{Protocol: "TCP", Port: int32(8899), TargetPort: intstr.FromInt(8899)}},
		},
	}
	_, err = svc.Client.CoreV1().Services(svc.Namespace).Create(&svc1)
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	svc2 := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-web",
			Namespace: "default",
			Labels:    map[string]string{appLabel: "test", processLabel: "web"},
		},
		Spec: v1.ServiceSpec{
			Selector: map[string]string{"name": "test-web"},
			Ports:    []v1.ServicePort{{Protocol: "TCP", Port: int32(8890), TargetPort: intstr.FromInt(8890)}},
		},
	}
	_, err = svc.Client.CoreV1().Services(svc.Namespace).Create(&svc2)
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	webService, err = svc.getWebService("test")
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	if webService.Name != "test-web" {
		t.Errorf("Expected service to be %v. Got %v.", svc2, webService)
	}

	if errCr := createCRD(&svc, "namespacedApp", "custom-namespace"); errCr != nil {
		t.Errorf("error creating CRD for test: %v", errCr)
	}
	svc3 := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "namespacedApp-web",
			Namespace: "custom-namespace",
			Labels:    map[string]string{appLabel: "namespacedApp", processLabel: "web"},
		},
		Spec: v1.ServiceSpec{
			Selector: map[string]string{"name": "namespacedApp-web"},
			Ports:    []v1.ServicePort{{Protocol: "TCP", Port: int32(8890), TargetPort: intstr.FromInt(8890)}},
		},
	}
	_, err = svc.Client.CoreV1().Services(svc3.Namespace).Create(&svc3)
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	webService, err = svc.getWebService("namespacedApp")
	if err != nil {
		t.Errorf("Expected err to be nil. Got %v.", err)
	}
	if webService.Name != "namespacedApp-web" {
		t.Errorf("Expected service to be %v. Got %v.", svc2, webService)
	}
}

func createCRD(svc *BaseService, app string, namespace string) error {
	_, err := svc.ExtensionsClient.ApiextensionsV1beta1().CustomResourceDefinitions().Create(&v1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "apps.tsuru.io"},
		Spec: v1beta1.CustomResourceDefinitionSpec{
			Group:   "tsuru.io",
			Version: "v1",
			Names: v1beta1.CustomResourceDefinitionNames{
				Plural:   "apps",
				Singular: "app",
				Kind:     "App",
				ListKind: "AppList",
			},
		},
	})
	if err != nil {
		return err
	}
	_, err = svc.TsuruClient.TsuruV1().Apps(svc.Namespace).Create(&tsuruv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: app},
		Spec: tsuruv1.AppSpec{
			NamespaceName: namespace,
		},
	})
	return err
}
