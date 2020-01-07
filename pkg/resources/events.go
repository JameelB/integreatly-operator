package resources

import (
	"fmt"

	integreatlyv1alpha1 "github.com/integr8ly/integreatly-operator/pkg/apis/integreatly/v1alpha1"

	"k8s.io/client-go/tools/record"
)

func emitEventStageCompleted(recorder record.EventRecorder, installation *integreatlyv1alpha1.Installation, stageName integreatlyv1alpha1.StageName) {
	stageStatus := installation.Status.Stages[stageName]
	if stageStatus == nil || stageStatus.Phase != integreatlyv1alpha1.PhaseCompleted {
		recorder.Event(installation, "Normal", integreatlyv1alpha1.EventInstallationCompleted, "Bootstrap stage reconciled successfully")
	}
}

func emitEventProductCompleted(recorder record.EventRecorder, installation *integreatlyv1alpha1.Installation, stageName integreatlyv1alpha1.StageName, productName integreatlyv1alpha1.ProductName) {
	productStatus := installation.Status.Stages[stageName].Products[productName]
	if productStatus == nil || productStatus.Status != integreatlyv1alpha1.PhaseCompleted {
		recorder.Event(installation, "Normal", integreatlyv1alpha1.EventInstallationCompleted, fmt.Sprintf("%s has reconciled successfully", productName))
	}
}

func emitEventProcessingError(recorder record.EventRecorder, installation *integreatlyv1alpha1.Installation, phase integreatlyv1alpha1.StatusPhase, errorMessage string) {
	if phase == integreatlyv1alpha1.PhaseFailed {
		recorder.Event(installation, "Warning", integreatlyv1alpha1.EventProcessingError, errorMessage)
	}
}
