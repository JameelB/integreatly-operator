package fuseonopenshift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sirupsen/logrus"

	integreatlyv1alpha1 "github.com/integr8ly/integreatly-operator/pkg/apis/integreatly/v1alpha1"
	"github.com/integr8ly/integreatly-operator/pkg/config"
	"github.com/integr8ly/integreatly-operator/pkg/resources"
	"github.com/integr8ly/integreatly-operator/pkg/resources/events"
	"github.com/integr8ly/integreatly-operator/pkg/resources/marketplace"

	samplesv1 "github.com/openshift/cluster-samples-operator/pkg/apis/samples/v1"

	corev1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	fuseOnOpenshiftNs      = "openshift"
	TemplatesBaseURL       = "https://raw.githubusercontent.com/jboss-fuse/application-templates/"
	templatesConfigMapName = "fuse-on-openshift-templates"
	imageStreamFileName    = "fis-image-streams.json"
)

var (
	quickstartTemplates = []string{
		"eap-camel-amq-template.json",
		"eap-camel-cdi-template.json",
		"eap-camel-cxf-jaxrs-template.json",
		"eap-camel-cxf-jaxws-template.json",
		"eap-camel-jpa-template.json",
		"karaf-camel-amq-template.json",
		"karaf-camel-log-template.json",
		"karaf-camel-rest-sql-template.json",
		"karaf-cxf-rest-template.json",
		"spring-boot-camel-amq-template.json",
		"spring-boot-camel-config-template.json",
		"spring-boot-camel-drools-template.json",
		"spring-boot-camel-infinispan-template.json",
		"spring-boot-camel-rest-3scale-template.json",
		"spring-boot-camel-rest-sql-template.json",
		"spring-boot-camel-teiid-template.json",
		"spring-boot-camel-template.json",
		"spring-boot-camel-xa-template.json",
		"spring-boot-camel-xml-template.json",
		"spring-boot-cxf-jaxrs-template.json",
		"spring-boot-cxf-jaxws-template.json",
	}
	consoleTemplates = []string{
		"fuse-console-cluster-os4.json",
		"fuse-console-namespace-os4.json",
		"fuse-apicurito.yml",
	}
)

type Reconciler struct {
	*resources.Reconciler
	coreClient    kubernetes.Interface
	Config        *config.FuseOnOpenshift
	ConfigManager config.ConfigReadWriter
	httpClient    http.Client
	logger        *logrus.Entry
	recorder      record.EventRecorder
	installation  *integreatlyv1alpha1.RHMI
}

func (r *Reconciler) GetPreflightObject(ns string) runtime.Object {
	return nil
}

func NewReconciler(configManager config.ConfigReadWriter, installation *integreatlyv1alpha1.RHMI, mpm marketplace.MarketplaceInterface, recorder record.EventRecorder) (*Reconciler, error) {
	config, err := configManager.ReadFuseOnOpenshift()
	if err != nil {
		return nil, fmt.Errorf("could not retrieve %s config: %w", integreatlyv1alpha1.ProductFuseOnOpenshift, err)
	}

	if config.GetNamespace() == "" {
		config.SetNamespace(fuseOnOpenshiftNs)
	}

	if err = config.Validate(); err != nil {
		return nil, fmt.Errorf("%s config is not valid: %w", integreatlyv1alpha1.ProductFuseOnOpenshift, err)
	}

	logger := logrus.NewEntry(logrus.StandardLogger())
	var httpClient http.Client

	return &Reconciler{
		ConfigManager: configManager,
		Config:        config,
		logger:        logger,
		httpClient:    httpClient,
		Reconciler:    resources.NewReconciler(mpm),
		recorder:      recorder,
		installation:  installation,
	}, nil
}

func (r *Reconciler) Reconcile(ctx context.Context, installation *integreatlyv1alpha1.RHMI, product *integreatlyv1alpha1.RHMIProductStatus, serverClient k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	phase, err := r.ReconcileFinalizer(ctx, serverClient, installation, string(r.Config.GetProductName()), func() (integreatlyv1alpha1.StatusPhase, error) {
		// get config of the samples operator
		clusterSampleCR := &samplesv1.Config{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cluster",
			},
		}

		if err := serverClient.Get(ctx, k8sclient.ObjectKey{Name: clusterSampleCR.Name}, clusterSampleCR); err != nil {
			// If the config cr for the sample operator is not found, the sample operator is not installed so no need to remove templates from it
			if k8errors.IsNotFound(err) {
				return integreatlyv1alpha1.PhaseCompleted, nil
			}
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get %s config for samples operator: %w", clusterSampleCR.Name, err)
		}

		// get fuse on openshift template config map
		templatesConfigMap, err := r.getTemplatesConfigMap(ctx, serverClient)
		if err != nil {
			if k8errors.IsNotFound(err) {
				return integreatlyv1alpha1.PhaseCompleted, nil
			}
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get config map %s from %s namespace: %w", templatesConfigMap.Name, templatesConfigMap.Namespace, err)
		}

		// remove fuse on openshift imagestreams from skippedImageStreams
		imageStreams, err := r.getImageStreamsFromConfigMap(templatesConfigMap)
		if err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get image streams from configmap %s", templatesConfigMap.Name)
		}
		imageStreamNames := r.getKeysFromMap(imageStreams)

		var skippedImageStreams []string
		for _, v := range clusterSampleCR.Spec.SkippedImagestreams {
			if !r.contains(imageStreamNames, v) {
				skippedImageStreams = append(skippedImageStreams, v)
			}
		}
		clusterSampleCR.Spec.SkippedImagestreams = skippedImageStreams

		// remove fuse on openshift templates from skippedTemplates
		templates, err := r.getTemplatesFromConfigMap(templatesConfigMap)
		if err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get templates from configmap %s: %w", templatesConfigMap.Name, err)
		}
		templateNames := r.getKeysFromMap(templates)

		var skippedTemplates []string
		for _, v := range clusterSampleCR.Spec.SkippedTemplates {
			if !r.contains(templateNames, v) {
				skippedTemplates = append(skippedTemplates, v)
			}
		}
		clusterSampleCR.Spec.SkippedTemplates = skippedTemplates

		// Update config of samples operator
		if err := serverClient.Update(ctx, clusterSampleCR); err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to update %s config for samples operator: %w", clusterSampleCR.Name, err)
		}

		// remove fuse on openshift templates
		if err := serverClient.Delete(ctx, templatesConfigMap); err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to delete %s configmap: %w", templatesConfigMap.Name, err)
		}

		return integreatlyv1alpha1.PhaseCompleted, nil
	})
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		events.HandleError(r.recorder, installation, phase, "Failed to reconcile finalizer", err)
		return phase, err
	}

	phase, err = r.reconcileConfigMap(ctx, serverClient)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		events.HandleError(r.recorder, installation, phase, "Failed to reconcile configmap", err)
		return phase, err
	}

	phase, err = r.reconcileImageStreams(ctx, serverClient)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		events.HandleError(r.recorder, installation, phase, "Failed to reconcile image streams", err)
		return phase, err
	}

	phase, err = r.reconcileTemplates(ctx, serverClient)
	if err != nil || phase != integreatlyv1alpha1.PhaseCompleted {
		events.HandleError(r.recorder, installation, phase, "Failed to reconcile templates", err)
		return phase, err
	}

	product.Version = r.Config.GetProductVersion()
	product.OperatorVersion = r.Config.GetOperatorVersion()

	events.HandleProductComplete(r.recorder, installation, integreatlyv1alpha1.ProductsStage, r.Config.GetProductName())
	logrus.Infof("%s successfully reconciled", integreatlyv1alpha1.ProductFuseOnOpenshift)
	return integreatlyv1alpha1.PhaseCompleted, nil
}

func (r *Reconciler) reconcileConfigMap(ctx context.Context, serverClient k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	logrus.Infoln("Reconciling Fuse on OpenShift templates config map")
	cfgMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templatesConfigMapName,
			Namespace: r.ConfigManager.GetOperatorNamespace(),
		},
	}

	cfgMap, err := r.getTemplatesConfigMap(ctx, serverClient)
	if err != nil && !k8errors.IsNotFound(err) {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get config map %s from %s namespace: %w", cfgMap.Name, cfgMap.Namespace, err)
	}

	configMapData := make(map[string]string)
	fileNames := []string{
		imageStreamFileName,
	}
	fileNames = append(fileNames, consoleTemplates...)

	for _, qn := range quickstartTemplates {
		fileNames = append(fileNames, "quickstarts/"+qn)
	}

	for _, fn := range fileNames {
		fileURL := TemplatesBaseURL + string(r.Config.GetProductVersion()) + "/" + fn
		content, err := r.getFileContentFromURL(fileURL)
		if err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get file contents of %s: %w", fn, err)
		}
		defer content.Close()

		data, err := ioutil.ReadAll(content)
		if err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to read contents of %s: %w", fn, err)
		}

		// Removes 'quickstarts/' from the key prefix as this is not a valid configmap data key
		key := strings.TrimPrefix(fn, "quickstarts/")

		// Write content of file to configmap
		configMapData[key] = string(data)
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, serverClient, cfgMap, func() error {
		cfgMap.Data = configMapData
		return nil
	}); err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("Error reconciling %s configmap: %w", cfgMap.Name, err)
	}

	return integreatlyv1alpha1.PhaseCompleted, nil
}

func (r *Reconciler) reconcileImageStreams(ctx context.Context, serverClient k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	logrus.Infoln("Reconciling Fuse on OpenShift imagestreams")
	cfgMap, err := r.getTemplatesConfigMap(ctx, serverClient)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get configmap %s from %s namespace: %w", cfgMap.Name, cfgMap.Data, err)
	}

	imageStreams, err := r.getImageStreamsFromConfigMap(cfgMap)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get image streams from configmap %s", cfgMap.Name)
	}

	imageStreamNames := r.getKeysFromMap(imageStreams)

	// Update the sample cluster sample operator CR to skip the Fuse on OpenShift image streams
	if err := r.updateClusterSampleCR(ctx, serverClient, "SkippedImagestreams", imageStreamNames); err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to update SkippedImagestreams in cluster sample custom resource: %w", err)
	}

	for isName, isObj := range imageStreams {
		if err := r.createResourceIfNotExist(ctx, serverClient, isObj); err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to create image stream %s: %w", isName, err)
		}
	}

	return integreatlyv1alpha1.PhaseCompleted, nil
}

func (r *Reconciler) reconcileTemplates(ctx context.Context, serverClient k8sclient.Client) (integreatlyv1alpha1.StatusPhase, error) {
	logrus.Infoln("Reconciling Fuse on OpenShift templates")
	cfgMap, err := r.getTemplatesConfigMap(ctx, serverClient)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get configmap %s from %s namespace: %w", cfgMap.Name, cfgMap.Data, err)
	}

	templates, err := r.getTemplatesFromConfigMap(cfgMap)
	if err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to get templates from configmap %s: %w", cfgMap.Name, err)
	}

	templateNames := r.getKeysFromMap(templates)

	// Update sample cluster operator CR to skip Fuse on OpenShift quickstart templates
	if err := r.updateClusterSampleCR(ctx, serverClient, "SkippedTemplates", templateNames); err != nil {
		return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to update SkippedTemplates in cluster sample custom resource: %w", err)
	}

	for name, obj := range templates {
		if err := r.createResourceIfNotExist(ctx, serverClient, obj); err != nil {
			return integreatlyv1alpha1.PhaseFailed, fmt.Errorf("failed to create image stream %s: %w", name, err)
		}
	}

	return integreatlyv1alpha1.PhaseCompleted, nil
}

func (r *Reconciler) getTemplatesConfigMap(ctx context.Context, serverClient k8sclient.Client) (*corev1.ConfigMap, error) {
	cfgMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      templatesConfigMapName,
			Namespace: r.ConfigManager.GetOperatorNamespace(),
		},
	}

	err := serverClient.Get(ctx, k8sclient.ObjectKey{Name: cfgMap.Name, Namespace: cfgMap.Namespace}, cfgMap)
	return cfgMap, err
}

func (r *Reconciler) createResourceIfNotExist(ctx context.Context, serverClient k8sclient.Client, resource runtime.Object) error {
	u, err := resources.UnstructuredFromRuntimeObject(resource)
	if err != nil {
		return fmt.Errorf("failed to get unstructured object of type %T from resource %s", resource, resource)
	}

	if err := serverClient.Get(ctx, k8sclient.ObjectKey{Name: u.GetName(), Namespace: u.GetNamespace()}, u); err != nil {
		if !k8errors.IsNotFound(err) {
			return fmt.Errorf("failed to get resource: %w", err)
		}
		if err := serverClient.Create(ctx, resource); err != nil {
			return fmt.Errorf("failed to create resource: %w", err)
		}
		return nil
	}

	if !r.resourceHasLabel(u.GetLabels(), "integreatly", "true") {
		if err := serverClient.Delete(ctx, resource); err != nil {
			return fmt.Errorf("failed to delete resource: %w", err)
		}
		if err := serverClient.Create(ctx, resource); err != nil {
			return fmt.Errorf("failed to create resource: %w", err)
		}
	}

	return nil
}

func (r *Reconciler) getFileContentFromURL(url string) (io.ReadCloser, error) {
	resp, err := r.httpClient.Get(url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	return nil, fmt.Errorf("failed to get file content from %s. Status: %d", url, resp.StatusCode)
}

func (r *Reconciler) getResourcesFromList(listObj map[string]interface{}) []interface{} {
	items := reflect.ValueOf(listObj["items"])

	var resources []interface{}

	for i := 0; i < items.Len(); i++ {
		resources = append(resources, items.Index(i).Interface())
	}

	return resources
}

func (r *Reconciler) getImageStreamsFromConfigMap(configMap *corev1.ConfigMap) (map[string]runtime.Object, error) {
	content := []byte(configMap.Data[imageStreamFileName])

	var fileContent map[string]interface{}
	if err := json.Unmarshal(content, &fileContent); err != nil {
		return nil, fmt.Errorf("failed to unmarshal contents of %s: %w", imageStreamFileName, err)
	}

	// The content of the imagestream file is a an object of kind List
	// Create the imagestreams seperately
	isList := r.getResourcesFromList(fileContent)
	imageStreams := make(map[string]runtime.Object)
	for _, is := range isList {
		jsonData, err := json.Marshal(is)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal data %s: %w", imageStreamFileName, err)
		}

		imageStreamRuntimeObj, err := resources.LoadKubernetesResource(jsonData, r.Config.GetNamespace())
		if err != nil {
			return nil, fmt.Errorf("failed to load kubernetes imagestream resource: %w", err)
		}

		// Get unstructured of image stream so we can retrieve the image stream name
		imageStreamUnstructured, err := resources.UnstructuredFromRuntimeObject(imageStreamRuntimeObj)
		if err != nil {
			return nil, fmt.Errorf("failed to parse runtime object to unstructured for imagestream: %w", err)
		}

		imageStreamName := imageStreamUnstructured.GetName()
		imageStreams[imageStreamName] = imageStreamRuntimeObj
	}
	return imageStreams, nil
}

func (r *Reconciler) getTemplatesFromConfigMap(configMap *corev1.ConfigMap) (map[string]runtime.Object, error) {
	var templateFiles []string
	templates := make(map[string]runtime.Object)

	templateFiles = append(templateFiles, consoleTemplates...)
	templateFiles = append(templateFiles, quickstartTemplates...)

	for _, fileName := range templateFiles {
		var err error
		content := []byte(configMap.Data[fileName])

		if filepath.Ext(fileName) == ".yml" || filepath.Ext(fileName) == ".yaml" {
			content, err = yaml.ToJSON(content)
			if err != nil {
				return nil, fmt.Errorf("failed to convert yaml to json %s: %w", fileName, err)
			}
		}

		templateRuntimeObj, err := resources.LoadKubernetesResource(content, r.Config.GetNamespace())
		if err != nil {
			return nil, fmt.Errorf("failed to load resource %s: %w", fileName, err)
		}

		templateUnstructured, err := resources.UnstructuredFromRuntimeObject(templateRuntimeObj)
		if err != nil {
			return nil, fmt.Errorf("failed to parse object: %w", err)
		}

		templateName := templateUnstructured.GetName()
		templates[templateName] = templateRuntimeObj
	}
	return templates, nil
}

func (r *Reconciler) updateClusterSampleCR(ctx context.Context, serverClient k8sclient.Client, key string, value []string) error {
	clusterSampleCR := &samplesv1.Config{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
	}

	if err := serverClient.Get(ctx, k8sclient.ObjectKey{Name: clusterSampleCR.Name}, clusterSampleCR); err != nil {
		// If cluster sample cr is not found, the cluster sample operator is not installed so no need to update it
		if k8errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if key == "SkippedImagestreams" {
		for _, v := range value {
			if !r.contains(clusterSampleCR.Spec.SkippedImagestreams, v) {
				clusterSampleCR.Spec.SkippedImagestreams = append(clusterSampleCR.Spec.SkippedImagestreams, v)
			}
		}
	}

	if key == "SkippedTemplates" {
		for _, v := range value {
			if !r.contains(clusterSampleCR.Spec.SkippedTemplates, v) {
				clusterSampleCR.Spec.SkippedTemplates = append(clusterSampleCR.Spec.SkippedTemplates, v)
			}
		}
	}

	if err := serverClient.Update(ctx, clusterSampleCR); err != nil {
		return err
	}

	return nil
}

func (r *Reconciler) getKeysFromMap(mapObj map[string]runtime.Object) []string {
	var keys []string

	for k := range mapObj {
		keys = append(keys, k)
	}
	return keys
}

func (r *Reconciler) resourceHasLabel(labels map[string]string, key, value string) bool {
	if val, ok := labels[key]; ok && val == value {
		return true
	}
	return false
}

func (r *Reconciler) contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}

	return false
}
