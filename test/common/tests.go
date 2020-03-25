package common

var (
	ALL_TESTS = []TestCase{
		// Add all the tests that should be executed in both e2e and osd suites here.
		// It is an array so the tests will be executed in the same order as they defined here.
		{"Verify CRD Exists", TestIntegreatlyCRDExists},
	}

	AFTER_INSTALL_TESTS = []TestCase{
		{"Verify Deployment resources have the expected replicas", TestDeploymentExpectedReplicas},
		{"Verify Deployment Config resources have the expected replicas", TestDeploymentConfigExpectedReplicas},
		{"Verify Stateful Set resources have the expected repliaces", TestStatefulSetsExpectedReplicas},
		{"Verify alerts exist", TestIntegreatlyAlertsExist},
		{"Verify dashboards exist", TestIntegreatlyDashboardsExist},
		{"Verify CRO Postgres CRs Successful", TestCROPostgresSuccessfulState},
		{"Verify CRO Redis CRs Successful", TestCRORedisSuccessfulState},
		{"Verify CRO BlobStorage CRs Successful", TestCROBlobStorageSuccessfulState},
		{"Verify PodDisruptionBudgets exist", TestIntegreatlyPodDisruptionBudgetsExist},
		{"Verify all products routes are created", TestIntegreatlyRoutesExist},
		{"Verify Codeready CRUDL permissions", TestCodereadyCrudlPermisssions},
	}
)
