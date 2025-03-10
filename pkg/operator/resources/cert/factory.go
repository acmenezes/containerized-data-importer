/*
Copyright 2020 The CDI Authors.

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

package cert

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"kubevirt.io/containerized-data-importer/pkg/operator/resources/utils"
)

const (
	// SignerLifetime is the default lifetime for the signer cert
	SignerLifetime = 48 * time.Hour
	// SignerRefresh is the default refresh time for the signer cert
	SignerRefresh = 24 * time.Hour

	// ServerLifetime is the default lifetime for the server cert
	ServerLifetime = 24 * time.Hour
	// ServerRefresh is the default refresh time for the server cert
	ServerRefresh = 12 * time.Hour

	// ClientLifetime is the default lifetime for the client cert
	ClientLifetime = 24 * time.Hour
	// ClientRefresh is the default refresh time for the client cert
	ClientRefresh = 12 * time.Hour
)

// FactoryArgs contains the required parameters to generate certs
type FactoryArgs struct {
	Namespace string

	SignerDuration *time.Duration
	// Duration to subtract from cert NotAfter value
	SignerRenewBefore *time.Duration

	ServerDuration *time.Duration
	// Duration to subtract from cert NotAfter value
	ServerRenewBefore *time.Duration

	ClientDuration *time.Duration
	// Duration to subtract from cert NotAfter value
	ClientRenewBefore *time.Duration
}

// CertificateConfig contains cert configuration data
type CertificateConfig struct {
	Lifetime time.Duration
	Refresh  time.Duration
}

// CertificateDefinition contains the data required to create/manage certtificate chains
type CertificateDefinition struct {
	// configurable by user
	Configurable bool

	// current CA key/cert
	SignerSecret *corev1.Secret
	SignerConfig CertificateConfig

	// all valid CA certs
	CertBundleConfigmap *corev1.ConfigMap

	// current key/cert for target
	TargetSecret *corev1.Secret
	TargetConfig CertificateConfig

	// only one of the following should be set
	// contains target key/cert for server
	TargetService *string
	// contains target user name
	TargetUser *string
}

// CreateCertificateDefinitions creates certificate definitions
func CreateCertificateDefinitions(args *FactoryArgs) []CertificateDefinition {
	defs := createCertificateDefinitions()
	for i := range defs {
		def := &defs[i]

		if def.SignerSecret != nil {
			addNamespace(args.Namespace, def.SignerSecret)
		}

		if def.CertBundleConfigmap != nil {
			addNamespace(args.Namespace, def.CertBundleConfigmap)
		}

		if def.TargetSecret != nil {
			addNamespace(args.Namespace, def.TargetSecret)
		}

		if !def.Configurable {
			continue
		}

		if args.SignerDuration != nil {
			def.SignerConfig.Lifetime = *args.SignerDuration
		}

		if args.SignerRenewBefore != nil {
			// convert to time from cert NotBefore
			def.SignerConfig.Refresh = def.SignerConfig.Lifetime - *args.SignerRenewBefore
		}

		if def.TargetService != nil {
			if args.ServerDuration != nil {
				def.TargetConfig.Lifetime = *args.ServerDuration
			}

			if args.ServerRenewBefore != nil {
				// convert to time from cert NotBefore
				def.TargetConfig.Refresh = def.TargetConfig.Lifetime - *args.ServerRenewBefore
			}
		}

		if def.TargetUser != nil {
			if args.ClientDuration != nil {
				def.TargetConfig.Lifetime = *args.ClientDuration
			}

			if args.ClientRenewBefore != nil {
				// convert to time from cert NotBefore
				def.TargetConfig.Refresh = def.TargetConfig.Lifetime - *args.ClientRenewBefore
			}
		}
	}

	return defs
}

func addNamespace(namespace string, obj metav1.Object) {
	if obj.GetNamespace() == "" {
		obj.SetNamespace(namespace)
	}
}

func createCertificateDefinitions() []CertificateDefinition {
	return []CertificateDefinition{
		{
			Configurable: true,
			SignerSecret: createTLSSecret("cdi-apiserver-signer"),
			SignerConfig: CertificateConfig{
				Lifetime: SignerLifetime,
				Refresh:  SignerRefresh,
			},
			CertBundleConfigmap: createConfigMap("cdi-apiserver-signer-bundle"),
			TargetSecret:        createTLSSecret("cdi-apiserver-server-cert"),
			TargetConfig: CertificateConfig{
				Lifetime: ServerLifetime,
				Refresh:  ServerRefresh,
			},
			TargetService: ptr.To("cdi-api"),
		},
		{
			Configurable: true,
			SignerSecret: createTLSSecret("cdi-uploadproxy-signer"),
			SignerConfig: CertificateConfig{
				Lifetime: SignerLifetime,
				Refresh:  SignerRefresh,
			},
			CertBundleConfigmap: createConfigMap("cdi-uploadproxy-signer-bundle"),
			TargetSecret:        createTLSSecret("cdi-uploadproxy-server-cert"),
			TargetConfig: CertificateConfig{
				Lifetime: ServerLifetime,
				Refresh:  ServerRefresh,
			},
			TargetService: ptr.To("cdi-uploadproxy"),
		},
		{
			Configurable: true,
			SignerSecret: createTLSSecret("cdi-uploadserver-signer"),
			SignerConfig: CertificateConfig{
				Lifetime: SignerLifetime,
				Refresh:  SignerRefresh,
			},
			CertBundleConfigmap: createConfigMap("cdi-uploadserver-signer-bundle"),
		},
		{
			Configurable: true,
			SignerSecret: createTLSSecret("cdi-uploadserver-client-signer"),
			SignerConfig: CertificateConfig{
				Lifetime: SignerLifetime,
				Refresh:  SignerRefresh,
			},
			CertBundleConfigmap: createConfigMap("cdi-uploadserver-client-signer-bundle"),
			TargetSecret:        createTLSSecret("cdi-uploadserver-client-cert"),
			TargetConfig: CertificateConfig{
				Lifetime: ClientLifetime,
				Refresh:  ClientRefresh,
			},
			TargetUser: ptr.To("client.upload-server.cdi.kubevirt.io"),
		},
	}
}

func createTLSSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: utils.ResourceBuilder.WithCommonLabels(nil),
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.key": []byte(""),
			"tls.crt": []byte(""),
		},
	}
}

func createConfigMap(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: utils.ResourceBuilder.WithCommonLabels(nil),
		},
	}
}
