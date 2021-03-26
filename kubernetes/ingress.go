// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kubernetes

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"strings"

	"github.com/opentracing/opentracing-go"
	"github.com/pkg/errors"
	"github.com/tsuru/kubernetes-router/router"
	v1 "k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	typedV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typedV1beta1 "k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
)

var (
	// AnnotationsACMEKey defines the common annotation used to enable acme-tls
	AnnotationsACMEKey = "kubernetes.io/tls-acme"
	labelCNameIngress  = "router.tsuru.io/is-cname-ingress"
	AnnotationsCNames  = "router.tsuru.io/cnames"

	defaultClassOpt          = "class"
	defaultOptsAsAnnotations = map[string]string{
		defaultClassOpt: "kubernetes.io/ingress.class",
	}
	defaultOptsAsAnnotationsDocs = map[string]string{
		defaultClassOpt: "Ingress class for the Ingress object",
	}
)

var (
	_ router.Router       = &IngressService{}
	_ router.RouterTLS    = &IngressService{}
	_ router.RouterStatus = &IngressService{}
)

// IngressService manages ingresses in a Kubernetes cluster that uses ingress-nginx
type IngressService struct {
	*BaseService
	DomainSuffix string

	// AnnotationsPrefix defines the common prefix used in the nginx ingress controller
	AnnotationsPrefix string
	// IngressClass defines the default ingress class used by the controller
	IngressClass string

	OptsAsAnnotations     map[string]string
	OptsAsAnnotationsDocs map[string]string
}

// Ensure creates or updates an Ingress resource to point it to either
// the only service or the one responsible for the process web
func (k *IngressService) Ensure(ctx context.Context, id router.InstanceID, o router.EnsureBackendOpts) error {
	span, ctx := opentracing.StartSpanFromContext(ctx, "ensureIngress")
	defer span.Finish()

	span.SetTag("cnames", o.CNames)
	span.SetTag("preserveOldCNames", o.PreserveOldCNames)

	ns, err := k.getAppNamespace(ctx, id.AppName)
	if err != nil {
		setSpanError(span, err)
		return err
	}
	ingressClient, err := k.ingressClient(ns)
	if err != nil {
		setSpanError(span, err)
		return err
	}
	isNew := false
	existingIngress, err := k.get(ctx, id)
	if err != nil {
		if !k8sErrors.IsNotFound(err) {
			setSpanError(span, err)
			return err
		}
		isNew = true
	}

	defaultTarget, err := k.getDefaultBackendTarget(o.Prefixes)
	if err != nil {
		setSpanError(span, err)
		return err
	}

	span.SetTag("defaultTarget.service", defaultTarget.Service)
	span.SetTag("defaultTarget.namespace", defaultTarget.Namespace)

	service, err := k.getWebService(ctx, id.AppName, *defaultTarget)
	if err != nil {
		setSpanError(span, err)
		return err
	}

	domainSuffix := o.Opts.DomainSuffix
	if k.DomainSuffix != "" {
		domainSuffix = k.DomainSuffix
	}

	var vhost string
	if len(o.Opts.Domain) > 0 {
		vhost = o.Opts.Domain
	} else if o.Opts.DomainPrefix == "" {
		vhost = fmt.Sprintf("%v.%v", id.AppName, domainSuffix)
	} else {
		vhost = fmt.Sprintf("%v.%v.%v", o.Opts.DomainPrefix, id.AppName, domainSuffix)
	}

	ingress := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k.ingressName(id),
			Namespace: ns,
			Labels: map[string]string{
				appBaseServiceNamespaceLabel: defaultTarget.Namespace,
				appBaseServiceNameLabel:      defaultTarget.Service,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(service, schema.GroupVersionKind{
					Group:   v1.SchemeGroupVersion.Group,
					Version: v1.SchemeGroupVersion.Version,
					Kind:    "Service",
				}),
			},
		},
		Spec: buildIngressSpec(vhost, o.Opts.Route, service),
	}
	k.fillIngressMeta(ingress, o.Opts, id)
	if len(o.CNames) > 1 {
		ingress.Annotations[AnnotationsCNames] = strings.Join(o.CNames, ",")
	}

	var existingCNames []string
	if existingIngress != nil {
		existingCNames = strings.Split(existingIngress.Annotations[AnnotationsCNames], ",")
	}
	_, cnamesToRemove := diffCNames(existingCNames, o.CNames)

	for _, cname := range o.CNames {
		err = k.ensureCNameBackend(ctx, ensureCNameBackendOpts{
			namespace:  ns,
			id:         id,
			cname:      cname,
			service:    service,
			routerOpts: o.Opts,
		})
		if err != nil {
			err = errors.Wrapf(err, "could not ensure CName: %q", cname)
			setSpanError(span, err)
			return err
		}
	}

	if o.PreserveOldCNames {
		cnamesToRemove = []string{}
	}
	span.LogKV("cnamesToRemove", cnamesToRemove)
	for _, cname := range cnamesToRemove {
		err = k.removeCNameBackend(ctx, ensureCNameBackendOpts{
			namespace:  ns,
			id:         id,
			cname:      cname,
			service:    service,
			routerOpts: o.Opts,
		})
		if err != nil {
			err = errors.Wrapf(err, "could not remove CName: %q", cname)
			setSpanError(span, err)
			return err
		}
	}
	if isNew {
		_, err = ingressClient.Create(ctx, ingress, metav1.CreateOptions{})
		if err != nil {
			setSpanError(span, err)
		}
		return err
	}

	hasChanges := ingressHasChanges(span, existingIngress, ingress)
	if hasChanges {
		ingress.ObjectMeta.ResourceVersion = existingIngress.ObjectMeta.ResourceVersion
		if existingIngress.Spec.Backend != nil {
			ingress.Spec.Backend = existingIngress.Spec.Backend
		}
		_, err = ingressClient.Update(ctx, ingress, metav1.UpdateOptions{})
		if err != nil {
			setSpanError(span, err)
		}
		return err
	}

	return nil
}

func buildIngressSpec(host, path string, service *v1.Service) v1beta1.IngressSpec {
	pathType := v1beta1.PathTypeImplementationSpecific
	return v1beta1.IngressSpec{
		Rules: []v1beta1.IngressRule{
			{
				Host: host,
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{
							{
								Path:     path,
								PathType: &pathType,
								Backend: v1beta1.IngressBackend{
									ServiceName: service.Name,
									ServicePort: intstr.FromInt(int(service.Spec.Ports[0].Port)),
								},
							},
						},
					},
				},
			},
		},
	}
}

func setSpanError(span opentracing.Span, err error) {
	span.SetTag("error", true)
	span.LogKV("error.message", err.Error())
}

type ensureCNameBackendOpts struct {
	namespace  string
	id         router.InstanceID
	cname      string
	service    *v1.Service
	routerOpts router.Opts
}

func (k *IngressService) ensureCNameBackend(ctx context.Context, opts ensureCNameBackendOpts) error {
	span, ctx := opentracing.StartSpanFromContext(ctx, "ensureIngressCName")
	defer span.Finish()

	span.SetTag("cname", opts.cname)

	ingressClient, err := k.ingressClient(opts.namespace)
	if err != nil {
		return err
	}
	isNew := false
	existingIngress, err := ingressClient.Get(ctx, k.ingressCName(opts.id, opts.cname), metav1.GetOptions{})
	if err != nil {
		if !k8sErrors.IsNotFound(err) {
			return err

		}
		isNew = true
	}

	ingress := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k.ingressCName(opts.id, opts.cname),
			Namespace: opts.namespace,
			Labels: map[string]string{
				appBaseServiceNamespaceLabel: opts.service.Namespace,
				appBaseServiceNameLabel:      opts.service.Name,
				labelCNameIngress:            "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(opts.service, schema.GroupVersionKind{
					Group:   v1.SchemeGroupVersion.Group,
					Version: v1.SchemeGroupVersion.Version,
					Kind:    "Service",
				}),
			},
		},
		Spec: buildIngressSpec(opts.cname, opts.routerOpts.Route, opts.service),
	}

	k.fillIngressMeta(ingress, opts.routerOpts, opts.id)

	if ingress.Annotations[AnnotationsACMEKey] == "true" {
		log.Printf("Acme-tls is enabled on ingress, creating TLS secret for CNAME.")
		ingress.Spec.TLS = append(ingress.Spec.TLS,
			[]v1beta1.IngressTLS{
				{
					Hosts:      []string{opts.cname},
					SecretName: k.secretName(opts.id, opts.cname),
				},
			}...)
	}

	if isNew {
		_, err = ingressClient.Create(ctx, ingress, metav1.CreateOptions{})
		return err
	}

	hasChanges := ingressHasChanges(span, existingIngress, ingress)
	if hasChanges {
		ingress.ObjectMeta.ResourceVersion = existingIngress.ObjectMeta.ResourceVersion
		if existingIngress.Spec.Backend != nil {
			ingress.Spec.Backend = existingIngress.Spec.Backend
		}
		_, err = ingressClient.Update(ctx, ingress, metav1.UpdateOptions{})
		return err
	}

	return nil
}

func (k *IngressService) removeCNameBackend(ctx context.Context, opts ensureCNameBackendOpts) error {
	span, ctx := opentracing.StartSpanFromContext(ctx, "removeIngressCName")
	defer span.Finish()

	span.SetTag("cname", opts.cname)

	ingressClient, err := k.ingressClient(opts.namespace)
	if err != nil {
		return err
	}
	err = ingressClient.Delete(ctx, k.ingressCName(opts.id, opts.cname), metav1.DeleteOptions{})
	if err != nil && !k8sErrors.IsNotFound(err) {
		return err
	}
	return nil
}

// Remove removes the Ingress resource associated with the app
func (k *IngressService) Remove(ctx context.Context, id router.InstanceID) error {
	ns, err := k.getAppNamespace(ctx, id.AppName)
	if err != nil {
		return err
	}
	client, err := k.ingressClient(ns)
	if err != nil {
		return err
	}
	deletePropagation := metav1.DeletePropagationForeground
	err = client.Delete(ctx, k.ingressName(id), metav1.DeleteOptions{PropagationPolicy: &deletePropagation})
	if k8sErrors.IsNotFound(err) {
		return nil
	}
	return err
}

// Get gets the address of the loadbalancer associated with
// the app Ingress resource
func (k *IngressService) GetAddresses(ctx context.Context, id router.InstanceID) ([]string, error) {
	ingress, err := k.get(ctx, id)

	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return []string{""}, nil
		}
		return nil, err
	}

	return []string{fmt.Sprintf("%v", ingress.Spec.Rules[0].Host)}, nil
}

func (k *IngressService) GetStatus(ctx context.Context, id router.InstanceID) (router.BackendStatus, string, error) {
	ingress, err := k.get(ctx, id)
	if err != nil {
		return router.BackendStatusNotReady, "", err
	}
	if isIngressReady(ingress) {
		return router.BackendStatusReady, "", nil
	}
	detail, err := k.getStatusForRuntimeObject(ctx, ingress.Namespace, "Ingress", ingress.UID)
	if err != nil {
		return router.BackendStatusNotReady, "", err
	}

	return router.BackendStatusNotReady, detail, nil
}

func (k *IngressService) get(ctx context.Context, id router.InstanceID) (*v1beta1.Ingress, error) {
	ns, err := k.getAppNamespace(ctx, id.AppName)
	if err != nil {
		return nil, err
	}
	client, err := k.ingressClient(ns)
	if err != nil {
		return nil, err
	}
	ingress, err := client.Get(ctx, k.ingressName(id), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return ingress, nil
}

func (k *IngressService) ingressClient(namespace string) (typedV1beta1.IngressInterface, error) {
	client, err := k.getClient()
	if err != nil {
		return nil, err
	}
	return client.ExtensionsV1beta1().Ingresses(namespace), nil
}

func (k *IngressService) secretClient(namespace string) (typedV1.SecretInterface, error) {
	client, err := k.getClient()
	if err != nil {
		return nil, err
	}
	return client.CoreV1().Secrets(namespace), nil
}

func (s *IngressService) ingressName(id router.InstanceID) string {
	return s.hashedResourceName(id, "kubernetes-router-"+id.AppName+"-ingress", 253)
}

func (s *IngressService) ingressCName(id router.InstanceID, cname string) string {
	return s.hashedResourceName(id, "kubernetes-router-cname-"+cname, 253)
}

func (s *IngressService) secretName(id router.InstanceID, certName string) string {
	return s.hashedResourceName(id, "kr-"+id.AppName+"-"+certName, 253)
}

func (s *IngressService) annotationWithPrefix(suffix string) string {
	if s.AnnotationsPrefix == "" {
		return suffix
	}
	return fmt.Sprintf("%v/%v", s.AnnotationsPrefix, suffix)
}

// AddCertificate adds certificates to app ingress
func (k *IngressService) AddCertificate(ctx context.Context, id router.InstanceID, certCname string, cert router.CertData) error {
	ns, err := k.getAppNamespace(ctx, id.AppName)
	if err != nil {
		return err
	}
	ingressClient, err := k.ingressClient(ns)
	if err != nil {
		return err
	}
	secret, err := k.secretClient(ns)
	if err != nil {
		return err
	}
	ingress, err := k.get(ctx, id)
	if err != nil {
		return err
	}
	tlsSecret := v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k.secretName(id, certCname),
			Namespace: ns,
			Labels: map[string]string{
				appLabel:    id.AppName,
				domainLabel: certCname,
			},
			Annotations: make(map[string]string),
		},
		Type: "kubernetes.io/tls",
		StringData: map[string]string{
			"tls.key": cert.Key,
			"tls.crt": cert.Certificate,
		},
	}
	retSecret, err := secret.Create(ctx, &tlsSecret, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	ingress.Spec.TLS = append(ingress.Spec.TLS,
		[]v1beta1.IngressTLS{
			{
				Hosts:      []string{certCname},
				SecretName: retSecret.Name,
			},
		}...)
	_, err = ingressClient.Update(ctx, ingress, metav1.UpdateOptions{})
	return err
}

// GetCertificate get certificates from app ingress
func (k *IngressService) GetCertificate(ctx context.Context, id router.InstanceID, certCname string) (*router.CertData, error) {
	ns, err := k.getAppNamespace(ctx, id.AppName)
	if err != nil {
		return nil, err
	}
	secret, err := k.secretClient(ns)
	if err != nil {
		return nil, err
	}

	retSecret, err := secret.Get(ctx, k.secretName(id, certCname), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	certificate := fmt.Sprintf("%s", retSecret.Data["tls.crt"])
	key := fmt.Sprintf("%s", retSecret.Data["tls.key"])
	return &router.CertData{Certificate: certificate, Key: key}, err
}

// RemoveCertificate delete certificates from app ingress
func (k *IngressService) RemoveCertificate(ctx context.Context, id router.InstanceID, certCname string) error {
	ns, err := k.getAppNamespace(ctx, id.AppName)
	if err != nil {
		return err
	}
	ingressClient, err := k.ingressClient(ns)
	if err != nil {
		return err
	}
	ingress, err := k.get(ctx, id)
	if err != nil {
		return err
	}
	secret, err := k.secretClient(ns)
	if err != nil {
		return err
	}
	for k := range ingress.Spec.TLS {
		for _, host := range ingress.Spec.TLS[k].Hosts {
			if strings.Compare(certCname, host) == 0 {
				ingress.Spec.TLS = append(ingress.Spec.TLS[:k], ingress.Spec.TLS[k+1:]...)
			}
		}
	}
	_, err = ingressClient.Update(ctx, ingress, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	err = secret.Delete(ctx, k.secretName(id, certCname), metav1.DeleteOptions{})
	return err
}

// SupportedOptions returns the supported options
func (s *IngressService) SupportedOptions(ctx context.Context) map[string]string {
	opts := map[string]string{
		router.Domain: "",
		router.Acme:   "",
		router.Route:  "",
	}
	docs := mergeMaps(defaultOptsAsAnnotationsDocs, s.OptsAsAnnotationsDocs)
	for k, v := range mergeMaps(defaultOptsAsAnnotations, s.OptsAsAnnotations) {
		opts[k] = v
		if docs[k] != "" {
			opts[k] = docs[k]
		}
	}
	return opts
}

func (s *IngressService) fillIngressMeta(i *v1beta1.Ingress, routerOpts router.Opts, id router.InstanceID) {
	if i.ObjectMeta.Labels == nil {
		i.ObjectMeta.Labels = map[string]string{}
	}
	if i.ObjectMeta.Annotations == nil {
		i.ObjectMeta.Annotations = map[string]string{}
	}
	for k, v := range s.Labels {
		i.ObjectMeta.Labels[k] = v
	}
	for k, v := range s.Annotations {
		i.ObjectMeta.Annotations[k] = v
	}
	i.ObjectMeta.Labels[appLabel] = id.AppName

	additionalOpts := routerOpts.AdditionalOpts
	if s.IngressClass != "" {
		additionalOpts = mergeMaps(routerOpts.AdditionalOpts, map[string]string{
			defaultClassOpt: s.IngressClass,
		})
	}

	optsAsAnnotations := mergeMaps(defaultOptsAsAnnotations, s.OptsAsAnnotations)
	for optName, optValue := range additionalOpts {
		labelName, ok := optsAsAnnotations[optName]
		if !ok {
			if strings.Contains(optName, "/") {
				labelName = optName
			} else {
				labelName = s.annotationWithPrefix(optName)
			}
		}
		if strings.HasSuffix(labelName, "-") {
			delete(i.ObjectMeta.Annotations, strings.TrimSuffix(labelName, "-"))
		} else {
			i.ObjectMeta.Annotations[labelName] = optValue
		}
	}
	if !routerOpts.Acme {
		return
	}
	if len(i.Spec.Rules) > 0 {
		i.Spec.TLS = []v1beta1.IngressTLS{
			{
				Hosts:      []string{i.Spec.Rules[0].Host},
				SecretName: s.secretName(id, i.Spec.Rules[0].Host),
			},
		}
	}
	i.ObjectMeta.Annotations[AnnotationsACMEKey] = "true"
}

func ingressHasChanges(span opentracing.Span, existing *v1beta1.Ingress, ing *v1beta1.Ingress) (hasChanges bool) {
	if !reflect.DeepEqual(existing.Spec, ing.Spec) {
		span.LogKV(
			"message", "ingress has changed the spec",
			"ingress", existing.Name,
		)
		return true
	}

	for key, value := range ing.Annotations {
		if existing.Annotations[key] != value {
			span.LogKV(
				"message", "ingress has changed the annotation",
				"ingress", existing.Name,
				"annotation", key,
				"existingValue", existing.Annotations[key],
				"newValue", value,
			)

			return true
		}
	}
	for key, value := range ing.Labels {
		if existing.Labels[key] != value {
			span.LogKV(
				"message", "ingress has changed the label",
				"ingress", existing.Name,
				"label", key,
				"existingValue", existing.Labels[key],
				"newValue", value,
			)
			return true
		}
	}
	span.LogKV(
		"message", "ingress has no changes",
		"ingress", existing.Name,
	)
	return false
}

func isIngressReady(ingress *v1beta1.Ingress) bool {
	if len(ingress.Status.LoadBalancer.Ingress) == 0 {
		return false
	}
	return ingress.Status.LoadBalancer.Ingress[0].IP != ""
}
