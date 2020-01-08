package installation

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/sirupsen/logrus"

	integreatlyv1alpha1 "github.com/integr8ly/integreatly-operator/pkg/apis/integreatly/v1alpha1"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/marketplace"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/products/config"
	"github.com/integr8ly/integreatly-operator/pkg/resources"

	oauthv1 "github.com/openshift/api/oauth/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func NewBootstrapReconciler(configManager config.ConfigReadWriter, installation *integreatlyv1alpha1.Installation, mpm marketplace.MarketplaceInterface, mgr manager.Manager) (*Reconciler, error) {
	return &Reconciler{
		ConfigManager: configManager,
		mpm:           mpm,
		installation:  installation,
		Reconciler:    resources.NewReconciler(mpm),
		recorder:      mgr.GetEventRecorderFor(string(integreatlyv1alpha1.BootstrapStage)),
	}, nil
}

type Reconciler struct {
	ConfigManager config.ConfigReadWriter
	Config        *config.ThreeScale
	mpm           marketplace.MarketplaceInterface
	installation  *integreatlyv1alpha1.Installation
	*resources.Reconciler
	recorder record.EventRecorder
}

func (r *Reconciler) GetPreflightObject(ns string) runtime.Object {
	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, installation *integreatlyv1alpha1.Installation, serverClient k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	logrus.Infof("Reconciling bootstrap stage")

	phase, err := r.reconcileOauthSecrets(ctx, serverClient)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		resources.EmitEventProcessingError(r.recorder, installation, phase, "Failed to reconcile oauth secrets", err)
		return phase, err
	}

	phase, err = r.retrieveConsoleUrlAndSubdomain(ctx, serverClient)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		resources.EmitEventProcessingError(r.recorder, installation, phase, "Failed to retrieve console url and subdomain", err)
		return phase, err
	}

	resources.EmitEventStageCompleted(r.recorder, installation, integreatlyv1alpha1.BootstrapStage)

	logrus.Infof("Bootstrap stage reconciled successfully")
	return integreatlyv1alpha1.PhaseCompleted, nil
}

func (r *Reconciler) reconcileOauthSecrets(ctx context.Context, serverClient k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	// List of products that require secret for OAuthClient
	productsList := []integreatlyv1alpha1.ProductName{
		integreatlyv1alpha1.ProductRHSSO,
		integreatlyv1alpha1.ProductRHSSOUser,
		integreatlyv1alpha1.Product3Scale,
	}

	oauthClientSecrets := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.ConfigManager.GetOauthClientsSecretName(),
			Namespace: r.ConfigManager.GetOperatorNamespace(),
		},
	}

	err := serverClient.Get(ctx, k8sclient.ObjectKey{Name: oauthClientSecrets.Name, Namespace: oauthClientSecrets.Namespace}, oauthClientSecrets)
	if !k8serr.IsNotFound(err) && err != nil {
		return integreatlyv1alpha1.PhaseFailed, err
	} else if k8serr.IsNotFound(err) {
		oauthClientSecrets.Data = map[string][]byte{}
	}

	for _, product := range productsList {
		if _, ok := oauthClientSecrets.Data[string(product)]; !ok {
			oauthClient := &oauthv1.OAuthClient{
				ObjectMeta: metav1.ObjectMeta{
					Name: r.installation.Spec.NamespacePrefix + string(product),
				},
			}
			err := serverClient.Get(ctx, k8sclient.ObjectKey{Name: oauthClientSecrets.Name}, oauthClient)
			if !k8serr.IsNotFound(err) && err != nil {
				return integreatlyv1alpha1.PhaseFailed, err
			} else if k8serr.IsNotFound(err) {
				oauthClientSecrets.Data[string(product)] = []byte(generateSecret(32))
			} else {
				// recover secret from existing OAuthClient object in case Secret object was deleted
				oauthClientSecrets.Data[string(product)] = []byte(oauthClient.Secret)
				logrus.Warningf("OAuth client secret for %s recovered from OAutchClient object", string(product))
			}
		}
	}

	oauthClientSecrets.ObjectMeta.ResourceVersion = ""
	err = resources.CreateOrUpdate(ctx, serverClient, oauthClientSecrets)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("Error reconciling OAuth clients secrets: %w", err)
	}
	logrus.Info("Bootstrap OAuth client secrets successfully reconciled")

	return integreatlyv1alpha1.PhaseCompleted, nil
}

func (r *Reconciler) retrieveConsoleUrlAndSubdomain(ctx context.Context, serverClient k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {

	consoleRouteCR, err := getConsoleRouteCR(ctx, serverClient)
	if err != nil {
		if k8serr.IsNotFound(err) {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("could not find CR route: %w", err)
		}
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("could not retrieve CR route: %w", err)
	}

	r.installation.Spec.MasterURL = consoleRouteCR.Status.Ingress[0].Host
	r.installation.Spec.RoutingSubdomain = consoleRouteCR.Status.Ingress[0].RouterCanonicalHostname

	return integreatlyv1alpha1.PhaseCompleted, nil

}

func getConsoleRouteCR(ctx context.Context, serverClient k8sclient.Client) (*routev1.Route, error) {
	// discover and set master url and routing subdomain
	consoleRouteCR := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "console",
			Namespace: "openshift-console",
		},
	}
	key := k8sclient.ObjectKey{
		Name:      consoleRouteCR.GetName(),
		Namespace: consoleRouteCR.GetNamespace(),
	}

	err := serverClient.Get(ctx, key, consoleRouteCR)
	if err != nil {
		return nil, err
	}
	return consoleRouteCR, nil
}

func generateSecret(length int) string {
	rand.Seed(time.Now().UnixNano())
	chars := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
	buf := make([]rune, length)
	for i := range buf {
		buf[i] = chars[rand.Intn(len(chars))]
	}
	return string(buf)
}
