package cloudresources

import (
	"context"
	"fmt"

	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/ownerutil"
	"github.com/sirupsen/logrus"

	crov1alpha1 "github.com/integr8ly/cloud-resource-operator/pkg/apis/integreatly/v1alpha1"
	crov1alpha1Types "github.com/integr8ly/cloud-resource-operator/pkg/apis/integreatly/v1alpha1/types"
	croUtil "github.com/integr8ly/cloud-resource-operator/pkg/resources"

	integreatlyv1alpha1 "github.com/integr8ly/integreatly-operator/pkg/apis/integreatly/v1alpha1"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/marketplace"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/products/config"
	"github.com/integr8ly/integreatly-operator/pkg/resources"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	defaultInstallationNamespace = "cloud-resources"
	defaultSubscriptionName      = "integreatly-cloud-resources"
	manifestPackage              = "integreatly-cloud-resources"
)

type Reconciler struct {
	Config        *config.CloudResources
	ConfigManager config.ConfigReadWriter
	mpm           marketplace.MarketplaceInterface
	logger        *logrus.Entry
	*resources.Reconciler
	recorder record.EventRecorder
}

func NewReconciler(configManager config.ConfigReadWriter, installation *integreatlyv1alpha1.Installation, mpm marketplace.MarketplaceInterface, mgr manager.Manager) (*Reconciler, error) {
	config, err := configManager.ReadCloudResources()
	if err != nil {
		return nil, fmt.Errorf("could not read cloud resources config: %w", err)
	}
	if config.GetNamespace() == "" {
		config.SetNamespace(installation.Spec.NamespacePrefix + defaultInstallationNamespace)
	}

	logger := logrus.WithFields(logrus.Fields{"product": config.GetProductName()})

	return &Reconciler{
		ConfigManager: configManager,
		Config:        config,
		mpm:           mpm,
		logger:        logger,
		Reconciler:    resources.NewReconciler(mpm),
		recorder:      mgr.GetEventRecorderFor(string(config.GetProductName())),
	}, nil
}

func (r *Reconciler) GetPreflightObject(ns string) runtime.Object {
	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, installation *integreatlyv1alpha1.Installation, product *integreatlyv1alpha1.InstallationProductStatus, client k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	ns := r.Config.GetNamespace()

	phase, err := r.ReconcileFinalizer(ctx, client, installation, string(r.Config.GetProductName()), func() (integreatlyv1alpha1.StatusPhase, error) {
		// ensure resources are cleaned up before deleting the namespace
		phase, err := r.doCloudResourcesExist(ctx, installation, client)
		if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
			return phase, err
		}

		// remove the namespace
		phase, err = resources.RemoveNamespace(ctx, installation, client, ns)
		if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
			return phase, err
		}
		return integreatlyv1alpha1.PhaseCompleted, nil
	})
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		if err != nil && phase == integreatlyv1alpha1.PhaseFailed {
			r.recorder.Event(installation, "Warning", integreatlyv1alpha1.EventProcessingError, fmt.Sprintf("Failed to reconcile finalizer: %s", err.Error()))
		}
		return phase, err
	}

	phase, err = r.ReconcileNamespace(ctx, ns, installation, client)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		if err != nil && phase == integreatlyv1alpha1.PhaseFailed {
			r.recorder.Event(installation, "Warning", integreatlyv1alpha1.EventProcessingError, fmt.Sprintf("Failed to reconcile %s namespace: %s", ns, err.Error()))
		}
		return phase, err
	}

	namespace, err := resources.GetNS(ctx, ns, client)
	if err != nil {
		r.recorder.Event(installation, "Warning", integreatlyv1alpha1.EventProcessingError, fmt.Sprintf("Failed to retrieve %s namespace: %s", ns, err.Error()))
		return integreatlyv1alpha1.PhaseFailed, err
	}

	phase, err = r.ReconcileSubscription(ctx, namespace, marketplace.Target{Pkg: defaultSubscriptionName, Channel: marketplace.IntegreatlyChannel, Namespace: r.Config.GetNamespace(), ManifestPackage: manifestPackage}, installation.Namespace, client)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		if err != nil && phase == integreatlyv1alpha1.PhaseFailed {
			r.recorder.Event(installation, "Warning", integreatlyv1alpha1.EventProcessingError, fmt.Sprintf("Failed to reconcile %s subscription: %s", defaultSubscriptionName, err.Error()))
		}
		return phase, err
	}

	phase, err = r.reconcileBackupsStorage(ctx, installation, client)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		return phase, err
	}

	product.Host = r.Config.GetHost()
	product.Version = r.Config.GetProductVersion()
	product.OperatorVersion = r.Config.GetOperatorVersion()

	croStatus := installation.Status.Stages[integreatlyv1alpha1.CloudResourcesStage].Products[r.Config.GetProductName()]
	if croStatus == nil || croStatus.Status != integreatlyv1alpha1.PhaseCompleted {
		r.recorder.Event(installation, "Normal", integreatlyv1alpha1.EventInstallationCompleted, fmt.Sprintf("%s has reconciled successfully", r.Config.GetProductName()))
	}

	r.logger.Infof("%s has reconciled successfully", r.Config.GetProductName())
	return integreatlyv1alpha1.PhaseCompleted, nil
}

// This only ensure that cloud resources no longer exists before proceeding to remove the cloud resource namespace.
// The deletion of the resources below are handled by the cro operator
func (r *Reconciler) doCloudResourcesExist(ctx context.Context, installation *integreatlyv1alpha1.Installation, client k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	r.logger.Info("ensuring cloud resources are cleaned up")

	// ensure postgres instances are cleaned up
	postgresInstances := &crov1alpha1.PostgresList{}
	postgresInstanceOpts := []k8sclient.ListOption{
		k8sclient.InNamespace(installation.Namespace),
	}
	err := client.List(ctx, postgresInstances, postgresInstanceOpts...)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to list postgres instances: %w", err)
	}
	if len(postgresInstances.Items) > 0 {
		r.logger.Info("deletion of postgres instances in progress")
		return integreatlyv1alpha1.PhaseInProgress, nil
	}

	// ensure redis instances are cleaned up
	redisInstances := &crov1alpha1.RedisList{}
	redisInstanceOpts := []k8sclient.ListOption{
		k8sclient.InNamespace(installation.Namespace),
	}
	err = client.List(ctx, redisInstances, redisInstanceOpts...)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to list redis instances: %w", err)
	}
	if len(redisInstances.Items) > 0 {
		r.logger.Info("deletion of redis instances in progress")
		return integreatlyv1alpha1.PhaseInProgress, nil
	}

	// ensure blob storage instances are cleaned up
	blobStorages := &crov1alpha1.BlobStorageList{}
	blobStorageOpts := []k8sclient.ListOption{
		k8sclient.InNamespace(installation.Namespace),
	}
	err = client.List(ctx, blobStorages, blobStorageOpts...)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to list blobStorage instances: %w", err)
	}
	if len(blobStorages.Items) > 0 {
		r.logger.Info("deletion of blob storage instances in progress")
		return integreatlyv1alpha1.PhaseInProgress, nil
	}

	// ensure blob storage instances are cleaned up
	smtpCredentialSets := &crov1alpha1.SMTPCredentialSetList{}
	smtpOpts := []k8sclient.ListOption{
		k8sclient.InNamespace(installation.Namespace),
	}
	err = client.List(ctx, smtpCredentialSets, smtpOpts...)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to list smtpCredentialSets instances: %w", err)
	}
	if len(smtpCredentialSets.Items) > 0 {
		r.logger.Info("deletion of smtp credential sets in progress")
		return integreatlyv1alpha1.PhaseInProgress, nil
	}

	// everything has been cleaned up, delete the ns
	return integreatlyv1alpha1.PhaseCompleted, nil
}

func (r *Reconciler) reconcileBackupsStorage(ctx context.Context, installation *integreatlyv1alpha1.Installation, client k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	blobStorageName := fmt.Sprintf("backups-blobstorage-%s", installation.Name)
	blobStorage, err := croUtil.ReconcileBlobStorage(ctx, client, installation.Spec.Type, "production", blobStorageName, installation.Namespace, r.ConfigManager.GetBackupsSecretName(), installation.Namespace, func(cr metav1.Object) error {
		ownerutil.EnsureOwner(cr, installation)
		return nil
	})
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to reconcile blob storage request: %w", err)
	}

	// wait for the blob storage cr to reconcile
	if blobStorage.Status.Phase != crov1alpha1Types.PhaseComplete {
		return integreatlyv1alpha1.PhaseAwaitingComponents, nil
	}

	return integreatlyv1alpha1.PhaseCompleted, nil
}
