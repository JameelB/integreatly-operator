package resources

import (
	"fmt"

	integreatlyv1alpha1 "github.com/integr8ly/integreatly-operator/pkg/apis/integreatly/v1alpha1"

	"k8s.io/client-go/tools/record"
)

// Emits a normal event upon successful completion of stage reconcile
func EmitEventStageCompleted(recorder record.EventRecorder, installation *integreatlyv1alpha1.Installation, stageName integreatlyv1alpha1.StageName) {
	stageStatus := installation.Status.Stages[stageName]
	if stageStatus == nil || stageStatus.Phase != integreatlyv1alpha1.PhaseCompleted {
		recorder.Event(installation, "Normal", integreatlyv1alpha1.EventInstallationCompleted, fmt.Sprintf("%s stage has reconciled successfully", stageName))
	}
}

// Emits a normal event upon successful completion of product installation
func EmitEventProductCompleted(recorder record.EventRecorder, installation *integreatlyv1alpha1.Installation, stageName integreatlyv1alpha1.StageName, productName integreatlyv1alpha1.ProductName) {
	productStatus := installation.Status.Stages[stageName].Products[productName]
	if productStatus == nil || productStatus.Status != integreatlyv1alpha1.PhaseCompleted {
		recorder.Event(installation, "Normal", integreatlyv1alpha1.EventInstallationCompleted, fmt.Sprintf("%s was installed successfully", productName))
	}
}

// Emits a warning event when a processing error occurs during reconcile. It is only emitted on phase failed
func EmitEventProcessingError(recorder record.EventRecorder, installation *integreatlyv1alpha1.Installation, phase integreatlyv1alpha1.StatusPhase, errorMessage string) {
	if phase == integreatlyv1alpha1.PhaseFailed {
		recorder.Event(installation, "Warning", integreatlyv1alpha1.EventProcessingError, errorMessage)
	}
}
