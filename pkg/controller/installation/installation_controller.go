package installation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	integreatlyv1alpha1 "github.com/integr8ly/integreatly-operator/pkg/apis/integreatly/v1alpha1"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/marketplace"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/products"
	"github.com/integr8ly/integreatly-operator/pkg/controller/installation/products/config"
	"github.com/integr8ly/integreatly-operator/pkg/resources"

	"github.com/operator-framework/operator-sdk/pkg/k8sutil"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	controllerruntime "sigs.k8s.io/controller-runtime"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	defaultInstallationConfigMapName = "integreatly-installation-config"
	defaultInstallationName          = "integreatly-operator"
	deletionFinalizer                = "foregroundDeletion"
)

// Add creates a new Installation Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, products []string) error {
	return add(mgr, newReconciler(mgr, products))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, products []string) reconcile.Reconciler {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	restConfig := controllerruntime.GetConfigOrDie()
	return &ReconcileInstallation{
		client:            mgr.GetClient(),
		scheme:            mgr.GetScheme(),
		restConfig:        restConfig,
		productsToInstall: products,
		context:           ctx,
		cancel:            cancel,
		mgr:               mgr,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("installation-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Creates a new managed install CR if it is not available
	cl, err := k8sclient.New(controllerruntime.GetConfigOrDie(), k8sclient.Options{})
	err = createsInstallationCR(context.TODO(), cl)
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Installation
	err = c.Watch(&source.Kind{Type: &integreatlyv1alpha1.Installation{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to secondary resource Pods and requeue the owner Installation
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &integreatlyv1alpha1.Installation{},
	})
	if err != nil {
		return err
	}

	return nil
}

func createsInstallationCR(ctx context.Context, serverClient k8sclient.Client) error {
	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		return err
	}

	logrus.Infof("Looking for installation CR in %s namespace", namespace)

	installationList := &integreatlyv1alpha1.InstallationList{}
	listOpts := []k8sclient.ListOption{
		k8sclient.InNamespace(namespace),
	}
	err = serverClient.List(ctx, installationList, listOpts...)
	if err != nil {
		return fmt.Errorf("Could not get a list of installation CR: %w", err)
	}

	// Creates installation CR in case there is none
	if len(installationList.Items) == 0 {

		logrus.Infof("Creating a %s installation CR as none CR installation was found in %s namespace", string(integreatlyv1alpha1.InstallationTypeManaged), namespace)

		installation := &integreatlyv1alpha1.Installation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultInstallationName,
				Namespace: namespace,
			},
			Spec: integreatlyv1alpha1.InstallationSpec{
				Type:            string(integreatlyv1alpha1.InstallationTypeManaged),
				NamespacePrefix: "rhmi-",
				SelfSignedCerts: false,
			},
		}

		err = serverClient.Create(ctx, installation)
		if err != nil {
			return fmt.Errorf("Could not create installation CR in %s namespace: %w", namespace, err)
		}
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileInstallation{}

// ReconcileInstallation reconciles a Installation object
type ReconcileInstallation struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client            k8sclient.Client
	scheme            *runtime.Scheme
	restConfig        *rest.Config
	productsToInstall []string
	context           context.Context
	cancel            context.CancelFunc
	mgr               manager.Manager
}

// Reconcile reads that state of the cluster for a Installation object and makes changes based on the state read
// and what is in the Installation.Spec
func (r *ReconcileInstallation) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	installation := &integreatlyv1alpha1.Installation{}
	err := r.client.Get(r.context, request.NamespacedName, installation)

	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	err, installType := InstallationTypeFactory(installation.Spec.Type, r.productsToInstall)
	if err != nil {
		return reconcile.Result{}, err
	}
	installationCfgMap := os.Getenv("INSTALLATION_CONFIG_MAP")
	if installationCfgMap == "" {
		installationCfgMap = installation.Spec.NamespacePrefix + defaultInstallationConfigMapName
	}

	configManager, err := config.NewManager(r.context, r.client, request.NamespacedName.Namespace, installationCfgMap, installation)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = resources.AddFinalizer(r.context, installation, r.client, deletionFinalizer)
	if err != nil {
		return reconcile.Result{}, err
	}

	// either not checked, or rechecking preflight checks
	if installation.Status.PreflightStatus == integreatlyv1alpha1.PreflightInProgress ||
		installation.Status.PreflightStatus == integreatlyv1alpha1.PreflightFail {
		return r.preflightChecks(installation, installType, configManager)
	}

	if installation.Status.Stages == nil {
		installation.Status.Stages = map[integreatlyv1alpha1.StageName]*integreatlyv1alpha1.InstallationStageStatus{}
	}

	// If the CR is being deleted, cancel the current context
	// and attempt to clean up the products with finalizers
	if installation.DeletionTimestamp != nil {
		// Cancel this context to kill all ongoing requests to the API
		// and use a new context to handle deletion logic
		r.cancel()

		// Clean up the products which have finalizers associated to them
		merr := &multiErr{}
		for _, productFinalizer := range installation.Finalizers {
			if !strings.Contains(productFinalizer, "integreatly") {
				continue
			}
			productName := strings.Split(productFinalizer, ".")[1]
			product := installation.GetProductStatusObject(integreatlyv1alpha1.ProductName(productName))
			reconciler, err := products.NewReconciler(product.Name, r.restConfig, configManager, installation, r.mgr)
			if err != nil {
				merr.Add(fmt.Errorf("Failed to build reconciler for product %s: %w", product.Name, err))
			}
			serverClient, err := k8sclient.New(r.restConfig, k8sclient.Options{})
			if err != nil {
				merr.Add(fmt.Errorf("Failed to create server client for %s: %w", product.Name, err))
			}
			phase, err := reconciler.Reconcile(context.TODO(), installation, product, serverClient)
			if err != nil {
				merr.Add(fmt.Errorf("Failed to reconcile product %s: %w", product.Name, err))
			}
			logrus.Infof("current phase for %s is: %s", product.Name, phase)
		}

		if len(merr.errors) == 0 && len(installation.Finalizers) == 1 && installation.Finalizers[0] == deletionFinalizer {
			err := resources.RemoveFinalizer(r.context, installation, r.client, deletionFinalizer)
			if err != nil {
				merr.Add(fmt.Errorf("Failed to remove finalizer: %w", err))
			}
			return reconcile.Result{}, nil
		}

		return reconcile.Result{
			Requeue:      true,
			RequeueAfter: time.Second * 10,
		}, nil
	}

	for _, stage := range installType.GetStages() {
		var err error
		var stagePhase integreatlyv1alpha1.StatusPhase
		if stage.Name == integreatlyv1alpha1.BootstrapStage {
			stagePhase, err = r.bootstrapStage(installation, configManager)
		} else {
			stagePhase, err = r.processStage(installation, &stage, configManager)
		}

		if installation.Status.Stages == nil {
			installation.Status.Stages = make(map[integreatlyv1alpha1.StageName]*integreatlyv1alpha1.InstallationStageStatus)
		}
		installation.Status.Stages[stage.Name] = &integreatlyv1alpha1.InstallationStageStatus{
			Name:     stage.Name,
			Phase:    stagePhase,
			Products: stage.Products,
		}
		if err != nil {
			return reconcile.Result{}, err
		}
		//don't move to next stage until current stage is complete
		if stagePhase != integreatlyv1alpha1.PhaseCompleted {
			break
		}
	}

	// UPDATE STATUS
	err = r.client.Status().Update(r.context, installation)
	if err != nil {
		// The 'Update' function can error if the resource has been updated by another process and the versions are not correct.
		if k8serr.IsConflict(err) {
			// If there is a conflict, requeue the resource and retry Update
			logrus.Info("Error updating Installation resource status. Requeue and retry.", err)
			return reconcile.Result{
				Requeue:      true,
				RequeueAfter: time.Second * 10,
			}, nil
		}

		logrus.Error(err, "error reconciling installation instance")
		return reconcile.Result{}, err
	}

	// UPDATE OBJECT
	err = r.client.Update(r.context, installation)
	if err != nil {
		// The 'Update' function can error if the resource has been updated by another process and the versions are not correct.
		if k8serr.IsConflict(err) {
			// If there is a conflict, requeue the resource and retry Update
			logrus.Info("Error updating Installation resource. Requeue and retry.", err)
			return reconcile.Result{
				Requeue:      true,
				RequeueAfter: time.Second * 10,
			}, nil
		}

		logrus.Error(err, "error reconciling installation instance")
		return reconcile.Result{}, err
	}

	return reconcile.Result{
		Requeue:      true,
		RequeueAfter: time.Second * 10,
	}, nil
}

func (r *ReconcileInstallation) preflightChecks(installation *integreatlyv1alpha1.Installation, installationType *Type, configManager *config.Manager) (reconcile.Result, error) {
	logrus.Info("Running preflight checks..")

	result := reconcile.Result{
		Requeue:      true,
		RequeueAfter: time.Second * 10,
	}
	requiredSecrets := []string{"github-oauth-secret"}
	for _, secretName := range requiredSecrets {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: installation.Namespace,
			},
		}
		if exists, err := resources.Exists(r.context, r.client, secret); err != nil {
			return result, err
		} else if !exists {
			preflightMessage := fmt.Sprintf("Could not find %s secret in integreatly-operator namespace: %s", secret.Name, installation.Namespace)
			installation.Status.PreflightStatus = integreatlyv1alpha1.PreflightFail
			installation.Status.PreflightMessage = preflightMessage
			logrus.Info(preflightMessage)
			_ = r.client.Status().Update(r.context, installation)
			return result, err
		}
	}

	namespaces := &corev1.NamespaceList{}
	err := r.client.List(r.context, namespaces)
	if err != nil {
		// could not list namespaces, keep trying
		logrus.Infof("error listing namespaces, will retry")
		return result, err
	}

	for _, ns := range namespaces.Items {
		products, err := r.checkNamespaceForProducts(ns, installation, installationType, configManager)
		if err != nil {
			// error searching for existing products, keep trying
			logrus.Infof("error looking for existing deployments, will retry")
			return result, err
		}
		if len(products) != 0 {
			//found one or more conflicting products
			installation.Status.PreflightStatus = integreatlyv1alpha1.PreflightFail
			installation.Status.PreflightMessage = "found conflicting packages: " + strings.Join(products, ", ") + ", in namespace: " + ns.GetName()
			logrus.Infof("found conflicting packages: " + strings.Join(products, ", ") + ", in namespace: " + ns.GetName())
			_ = r.client.Status().Update(r.context, installation)
			return result, err
		}
	}

	installation.Status.PreflightStatus = integreatlyv1alpha1.PreflightSuccess
	installation.Status.PreflightMessage = "preflight checks passed"
	_ = r.client.Status().Update(r.context, installation)
	return result, nil
}

func (r *ReconcileInstallation) checkNamespaceForProducts(ns corev1.Namespace, installation *integreatlyv1alpha1.Installation, installationType *Type, configManager *config.Manager) ([]string, error) {
	foundProducts := []string{}
	// new client to avoid caching issues
	serverClient, _ := k8sclient.New(r.restConfig, k8sclient.Options{})
	for _, stage := range installationType.Stages {
		for _, product := range stage.Products {
			reconciler, err := products.NewReconciler(product.Name, r.restConfig, configManager, installation, r.mgr)
			if err != nil {
				return foundProducts, err
			}
			search := reconciler.GetPreflightObject(ns.Name)
			if search == nil {
				continue
			}
			exists, err := resources.Exists(r.context, serverClient, search)
			if err != nil {
				return foundProducts, err
			} else if exists {
				foundProducts = append(foundProducts, string(product.Name))
			}
		}
	}
	return foundProducts, nil
}

func (r *ReconcileInstallation) bootstrapStage(installation *integreatlyv1alpha1.Installation, configManager config.ConfigReadWriter) (integreatlyv1alpha1.StatusPhase, error) {
	mpm := marketplace.NewManager()

	reconciler, err := NewBootstrapReconciler(configManager, installation, mpm, r.mgr)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to build a reconciler for Bootstrap: %w", err)
	}
	serverClient, err := k8sclient.New(r.restConfig, k8sclient.Options{})
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("could not create server client: %w", err)
	}
	phase, err := reconciler.Reconcile(r.context, installation, serverClient)
	if err != nil || phase == integreatlyv1alpha1.PhaseFailed {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("Bootstrap stage reconcile failed: %w", err)
	}

	return phase, nil
}

func (r *ReconcileInstallation) processStage(installation *integreatlyv1alpha1.Installation, stage *Stage, configManager config.ConfigReadWriter) (integreatlyv1alpha1.StatusPhase, error) {
	incompleteStage := false
	var mErr error
	for _, product := range stage.Products {
		reconciler, err := products.NewReconciler(product.Name, r.restConfig, configManager, installation, r.mgr)
		if err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to build a reconciler for %s: %w", product.Name, err)
		}
		serverClient, err := k8sclient.New(r.restConfig, k8sclient.Options{})
		if err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("could not create server client: %w", err)
		}
		product.Status, err = reconciler.Reconcile(r.context, installation, product, serverClient)
		if err != nil {
			if mErr == nil {
				mErr = &multiErr{}
			}
			mErr.(*multiErr).Add(fmt.Errorf("failed installation of %s: %w", product.Name, err))
		}
		//found an incomplete product
		if !(product.Status == integreatlyv1alpha1.PhaseCompleted) {
			incompleteStage = true
		}
	}

	//some products in this stage have not installed successfully yet
	if incompleteStage {
		return integreatlyv1alpha1.PhaseInProgress, mErr
	}
	return integreatlyv1alpha1.PhaseCompleted, mErr
}

type multiErr struct {
	errors []string
}

func (mer *multiErr) Error() string {
	return "product installation errors : " + strings.Join(mer.errors, ":")
}

func (mer *multiErr) Add(err error) {
	if mer.errors == nil {
		mer.errors = []string{}
	}
	mer.errors = append(mer.errors, err.Error())
}
