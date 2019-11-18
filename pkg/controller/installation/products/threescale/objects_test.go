package threescale

import (
	"bytes"
	"fmt"

	"github.com/integr8ly/integreatly-operator/pkg/apis/integreatly/v1alpha1"
	"github.com/integr8ly/integreatly-operator/pkg/resources"

	aerogearv1 "github.com/integr8ly/integreatly-operator/pkg/apis/aerogear/v1alpha1"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/products/rhsso"
	appsv1 "github.com/openshift/api/apps/v1"
	usersv1 "github.com/openshift/api/user/v1"
	coreosv1alpha1 "github.com/operator-framework/operator-lifecycle-manager/pkg/api/apis/operators/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	testRhssoNamespace = "test-rhsso"
	testRhssoRealm     = "test-realm"
	testRhssoURL       = "https://test.rhsso.url"
)

var configManagerConfigMap = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name: "integreatly-installation-config",
	},
	Data: map[string]string{
		"rhsso": fmt.Sprintf("NAMESPACE: %s\nREALM: %s\nURL: %s", testRhssoNamespace, testRhssoRealm, testRhssoURL),
	},
}

var OpenshiftDockerSecret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name:      resources.DefaultOriginPullSecretName,
		Namespace: resources.DefaultOriginPullSecretNamespace,
	},
}

var ComponentDockerSecret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name:      resources.DefaultOriginPullSecretName,
		Namespace: resources.DefaultOriginPullSecretNamespace,
	},
}

var installPlanFor3ScaleSubscription = &coreosv1alpha1.InstallPlan{
	ObjectMeta: metav1.ObjectMeta{
		Name: "installplan-for-3scale",
	},
	Status: coreosv1alpha1.InstallPlanStatus{
		Phase: coreosv1alpha1.InstallPlanPhaseComplete,
	},
}

var s3BucketSecret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name: s3BucketSecretName,
	},
}

var s3CredentialsSecret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name: s3CredentialsSecretName,
	},
}

var keycloakrealm = &aerogearv1.KeycloakRealm{
	ObjectMeta: metav1.ObjectMeta{
		Name:      testRhssoRealm,
		Namespace: testRhssoNamespace,
	},
	Spec: aerogearv1.KeycloakRealmSpec{
		KeycloakApiRealm: &aerogearv1.KeycloakApiRealm{
			Users: []*aerogearv1.KeycloakUser{
				rhsso.CustomerAdminUser,
				rhssoTest1,
				rhssoTest2,
			},
			Clients: []*aerogearv1.KeycloakClient{},
		},
	},
}

var threeScaleAdminDetailsSecret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name: "system-seed",
	},
	Data: map[string][]byte{
		"ADMIN_USER":  bytes.NewBufferString(threeScaleDefaultAdminUser.UserDetails.Username).Bytes(),
		"ADMIN_EMAIL": bytes.NewBufferString(threeScaleDefaultAdminUser.UserDetails.Email).Bytes(),
	},
}

var threeScaleServiceDiscoveryConfigMap = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name: "system",
	},
	Data: map[string]string{
		"service_discovery.yml": "",
	},
}

var threeScaleDefaultAdminUser = &User{
	UserDetails: UserDetails{
		Id:       1,
		Email:    "not" + rhsso.CustomerAdminUser.Email,
		Username: "not" + rhsso.CustomerAdminUser.UserName,
		Role:     adminRole,
	},
}

var rhssoTest1 = &aerogearv1.KeycloakUser{
	KeycloakApiUser: &aerogearv1.KeycloakApiUser{
		UserName: "test1",
		Email:    "test1@example.com",
	},
}

var rhssoTest2 = &aerogearv1.KeycloakUser{
	KeycloakApiUser: &aerogearv1.KeycloakApiUser{
		UserName: "test2",
		Email:    "test2@example.com",
	},
}

var testDedicatedAdminsGroup = &usersv1.Group{
	ObjectMeta: metav1.ObjectMeta{
		Name: "rhmi-admins",
	},
	Users: []string{
		rhssoTest1.UserName,
	},
}

var systemApp = appsv1.DeploymentConfig{
	ObjectMeta: metav1.ObjectMeta{
		Name: "system-app",
	},
	Status: appsv1.DeploymentConfigStatus{
		LatestVersion: 1,
	},
}

var systemSidekiq = appsv1.DeploymentConfig{
	ObjectMeta: metav1.ObjectMeta{
		Name: "system-sidekiq",
	},
	Status: appsv1.DeploymentConfigStatus{
		LatestVersion: 1,
	},
}

var successfulTestAppsV1Objects = map[string]*appsv1.DeploymentConfig{
	systemApp.Name:     &systemApp,
	systemSidekiq.Name: &systemSidekiq,
}

var systemEnvConfigMap = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "system-environment",
		Namespace: defaultInstallationNamespace,
	},
}

var oauthClientSecrets = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name: "oauth-client-secrets",
	},
	Data: map[string][]byte{
		"3scale": bytes.NewBufferString("test").Bytes(),
	},
}

var installation = &v1alpha1.Installation{
	ObjectMeta: metav1.ObjectMeta{
		Name:       "test-installation",
		Namespace:  "integreatly-operator-namespace",
		Finalizers: []string{"finalizer.3scale.integreatly.org"},
	},
	TypeMeta: metav1.TypeMeta{
		APIVersion: v1alpha1.SchemeGroupVersion.String(),
	},
	Spec: v1alpha1.InstallationSpec{
		MasterURL:        "https://console.apps.example.com",
		RoutingSubdomain: "apps.example.com",
	},
}

func getSuccessfullTestPreReqs(integreatlyOperatorNamespace, threeScaleInstallationNamepsace string) []runtime.Object {
	configManagerConfigMap.Namespace = integreatlyOperatorNamespace
	s3BucketSecret.Namespace = integreatlyOperatorNamespace
	s3CredentialsSecret.Namespace = integreatlyOperatorNamespace
	threeScaleAdminDetailsSecret.Namespace = threeScaleInstallationNamepsace
	threeScaleServiceDiscoveryConfigMap.Namespace = threeScaleInstallationNamepsace
	systemEnvConfigMap.Namespace = threeScaleInstallationNamepsace
	oauthClientSecrets.Namespace = integreatlyOperatorNamespace
	installation.Namespace = integreatlyOperatorNamespace

	return []runtime.Object{
		s3BucketSecret,
		s3CredentialsSecret,
		keycloakrealm,
		configManagerConfigMap,
		threeScaleAdminDetailsSecret,
		threeScaleServiceDiscoveryConfigMap,
		systemEnvConfigMap,
		testDedicatedAdminsGroup,
		OpenshiftDockerSecret,
		oauthClientSecrets,
		installation,
	}
}
