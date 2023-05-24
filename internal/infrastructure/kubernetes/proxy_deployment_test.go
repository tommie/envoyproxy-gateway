// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package kubernetes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	egcfgv1a1 "github.com/envoyproxy/gateway/api/config/v1alpha1"
	"github.com/envoyproxy/gateway/internal/envoygateway"
	"github.com/envoyproxy/gateway/internal/envoygateway/config"
	"github.com/envoyproxy/gateway/internal/gatewayapi"
	"github.com/envoyproxy/gateway/internal/infrastructure/kubernetes/proxy"
	"github.com/envoyproxy/gateway/internal/ir"
)

const (
	// envoyContainerName is the name of the Envoy container.
	envoyContainerName = "envoy"
)

func deploymentWithImage(deploy *appsv1.Deployment, image string) *appsv1.Deployment {
	dCopy := deploy.DeepCopy()
	for i, c := range dCopy.Spec.Template.Spec.Containers {
		if c.Name == envoyContainerName {
			dCopy.Spec.Template.Spec.Containers[i].Image = image
		}
	}
	return dCopy
}

func TestCreateOrUpdateProxyDeployment(t *testing.T) {
	cfg, err := config.New()
	require.NoError(t, err)

	infra := ir.NewInfra()
	infra.Proxy.GetProxyMetadata().Labels[gatewayapi.OwningGatewayNamespaceLabel] = "default"
	infra.Proxy.GetProxyMetadata().Labels[gatewayapi.OwningGatewayNameLabel] = infra.Proxy.Name

	r := proxy.NewResourceRender(cfg.Namespace, infra.GetProxyInfra())
	deploy, err := r.Deployment()
	require.NoError(t, err)

	// Extract what the DaemonSet would look like, had it been configured.
	infra.Proxy.GetProxyConfig().Spec.Provider = &egcfgv1a1.EnvoyProxyProvider{
		Type: egcfgv1a1.ProviderTypeKubernetes,
		Kubernetes: &egcfgv1a1.EnvoyProxyKubernetesProvider{
			EnvoyDaemonSet: &egcfgv1a1.KubernetesDaemonSetSpec{},
		},
	}
	r = proxy.NewResourceRender(cfg.Namespace, infra.GetProxyInfra())
	wantDaemonSet, err := r.DaemonSet()
	require.NoError(t, err)
	infra.Proxy.GetProxyConfig().Spec.Provider = nil

	testCases := []struct {
		name          string
		in            *ir.Infra
		current       *appsv1.Deployment
		want          *appsv1.Deployment
		wantDaemonSet *appsv1.DaemonSet
	}{
		{
			name: "create deployment",
			in:   infra,
			want: deploy,
		},
		{
			name:    "deployment exists",
			in:      infra,
			current: deploy,
			want:    deploy,
		},
		{
			name: "update deployment image",
			in: &ir.Infra{
				Proxy: &ir.ProxyInfra{
					Metadata: &ir.InfraMetadata{
						Labels: map[string]string{
							gatewayapi.OwningGatewayNamespaceLabel: "default",
							gatewayapi.OwningGatewayNameLabel:      infra.Proxy.Name,
						},
					},
					Config: &egcfgv1a1.EnvoyProxy{
						Spec: egcfgv1a1.EnvoyProxySpec{
							Provider: &egcfgv1a1.EnvoyProxyProvider{
								Type: egcfgv1a1.ProviderTypeKubernetes,
								Kubernetes: &egcfgv1a1.EnvoyProxyKubernetesProvider{
									EnvoyDeployment: &egcfgv1a1.KubernetesDeploymentSpec{
										Container: &egcfgv1a1.KubernetesContainerSpec{
											Image: pointer.String("envoyproxy/envoy-dev:v1.2.3"),
										},
									},
								},
							},
						},
					},
					Name:      ir.DefaultProxyName,
					Listeners: ir.NewProxyListeners(),
				},
			},
			current: deploy,
			want:    deploymentWithImage(deploy, "envoyproxy/envoy-dev:v1.2.3"),
		},
		{
			name: "update to daemon set",
			in: &ir.Infra{
				Proxy: &ir.ProxyInfra{
					Metadata: &ir.InfraMetadata{
						Labels: map[string]string{
							gatewayapi.OwningGatewayNamespaceLabel: "default",
							gatewayapi.OwningGatewayNameLabel:      infra.Proxy.Name,
						},
					},
					Config: &egcfgv1a1.EnvoyProxy{
						Spec: egcfgv1a1.EnvoyProxySpec{
							Provider: &egcfgv1a1.EnvoyProxyProvider{
								Type: egcfgv1a1.ProviderTypeKubernetes,
								Kubernetes: &egcfgv1a1.EnvoyProxyKubernetesProvider{
									EnvoyDaemonSet: &egcfgv1a1.KubernetesDaemonSetSpec{},
								},
							},
						},
					},
					Name:      ir.DefaultProxyName,
					Listeners: ir.NewProxyListeners(),
				},
			},
			current:       deploy,
			wantDaemonSet: wantDaemonSet,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var cli client.Client
			if tc.current != nil {
				cli = fakeclient.NewClientBuilder().WithScheme(envoygateway.GetScheme()).WithObjects(tc.current).Build()
			} else {
				cli = fakeclient.NewClientBuilder().WithScheme(envoygateway.GetScheme()).Build()
			}

			kube := NewInfra(cli, cfg)
			r := proxy.NewResourceRender(kube.Namespace, tc.in.GetProxyInfra())
			err := kube.createOrUpdatePodSet(context.Background(), r)
			require.NoError(t, err)

			actual := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: kube.Namespace,
					Name:      proxy.ExpectedResourceHashedName(tc.in.Proxy.Name),
				},
			}
			if tc.want != nil {
				require.NoError(t, kube.Client.Get(context.Background(), client.ObjectKeyFromObject(actual), actual))
				require.Equal(t, tc.want.Spec, actual.Spec)
			} else {
				require.Error(t, kube.Client.Get(context.Background(), client.ObjectKeyFromObject(actual), actual))
			}

			if tc.wantDaemonSet != nil {
				actual := &appsv1.DaemonSet{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: kube.Namespace,
						Name:      proxy.ExpectedResourceHashedName(tc.in.Proxy.Name),
					},
				}
				require.NoError(t, kube.Client.Get(context.Background(), client.ObjectKeyFromObject(actual), actual))
				require.Equal(t, tc.wantDaemonSet.Spec, actual.Spec)
			}
		})
	}
}

func TestDeleteProxyDeployment(t *testing.T) {
	cli := fakeclient.NewClientBuilder().WithScheme(envoygateway.GetScheme()).WithObjects().Build()
	cfg, err := config.New()
	require.NoError(t, err)

	testCases := []struct {
		name   string
		expect bool
	}{
		{
			name:   "delete deployment",
			expect: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			kube := NewInfra(cli, cfg)

			infra := ir.NewInfra()
			infra.Proxy.GetProxyMetadata().Labels[gatewayapi.OwningGatewayNamespaceLabel] = "default"
			infra.Proxy.GetProxyMetadata().Labels[gatewayapi.OwningGatewayNameLabel] = infra.Proxy.Name
			r := proxy.NewResourceRender(kube.Namespace, infra.GetProxyInfra())

			err := kube.createOrUpdatePodSet(context.Background(), r)
			require.NoError(t, err)

			err = kube.deleteDeployment(context.Background(), r)
			require.NoError(t, err)
		})
	}
}
