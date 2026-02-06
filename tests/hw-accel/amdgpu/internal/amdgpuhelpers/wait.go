package amdgpuhelpers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nodes"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	amdgpuparams "github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/amdgpu/params"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// devicePluginStatus holds the status of device-plugin/node-labeller pods.
type devicePluginStatus struct {
	found      bool
	allRunning bool
	count      int
}

// checkDevicePluginPods evaluates the status of device-plugin and node-labeller pods.
func checkDevicePluginPods(podList *corev1.PodList) devicePluginStatus {
	status := devicePluginStatus{allRunning: true}

	for i := range podList.Items {
		podItem := &podList.Items[i]

		if !isDevicePluginOrNodeLabeller(podItem.Name) {
			continue
		}

		status.found = true

		if podItem.Status.Phase == corev1.PodRunning {
			status.count++
		} else {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof(
				"Pod %s is %s", podItem.Name, podItem.Status.Phase)

			status.allRunning = false
		}
	}

	return status
}

// isDevicePluginOrNodeLabeller checks if a pod name matches device-plugin or node-labeller patterns.
func isDevicePluginOrNodeLabeller(name string) bool {
	return strings.Contains(name, "device-plugin") ||
		(strings.Contains(name, "node-labeller") && !strings.Contains(name, "build"))
}

// driverInitResult represents the result of checking a driver-init container.
type driverInitResult struct {
	ready bool
	err   error
}

// checkDriverInitContainer checks the status of a driver-init container.
func checkDriverInitContainer(initStatus corev1.ContainerStatus, podName string) driverInitResult {
	switch {
	case initStatus.State.Terminated != nil:
		if initStatus.State.Terminated.ExitCode == 0 {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof(
				"driver-init completed for pod %s", podName)

			return driverInitResult{ready: true}
		}

		return driverInitResult{
			err: fmt.Errorf("driver-init failed for pod %s with exit code %d",
				podName, initStatus.State.Terminated.ExitCode),
		}

	case initStatus.State.Running != nil:
		klog.V(amdgpuparams.AMDGPULogLevel).Infof(
			"driver-init still running for pod %s (waiting for amdgpu module)...", podName)

		return driverInitResult{ready: false}

	case initStatus.State.Waiting != nil:
		klog.V(amdgpuparams.AMDGPULogLevel).Infof(
			"driver-init waiting for pod %s: %s", podName, initStatus.State.Waiting.Reason)

		return driverInitResult{ready: false}

	default:
		return driverInitResult{ready: false}
	}
}

// WaitForClusterStabilityAfterDeviceConfig waits for the cluster to stabilize after DeviceConfig creation.
func WaitForClusterStabilityAfterDeviceConfig(apiClients *clients.Settings) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Info("Waiting for cluster stability after DeviceConfig creation")

	if err := WaitForClusterStability(apiClients, amdgpuparams.ClusterStabilityTimeout); err != nil {
		klog.Errorf("Cluster stability check after DeviceConfig creation failed: %v", err)

		return fmt.Errorf("cluster stability check after DeviceConfig creation failed: %w", err)
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Info("Cluster is stable after DeviceConfig creation")

	return nil
}

// WaitForClusterStability efficiently waits for all nodes to be ready and all cluster operators to be stable.
// It uses polling with connection error resilience to handle SNO reboots.
func WaitForClusterStability(apiClients *clients.Settings, timeout time.Duration) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Infof("Waiting up to %v for cluster to stabilize...", timeout)

	return wait.PollUntilContextTimeout(
		context.Background(),
		amdgpuparams.ConnectionRetryInterval,
		timeout,
		true,
		func(ctx context.Context) (bool, error) {
			// 1. Check Node Readiness
			nodesReady, err := checkNodesReady(apiClients)
			if err != nil || !nodesReady {
				return false, err
			}

			// 2. Check Cluster Operators
			operatorsReady, err := checkOperatorsReady(ctx, apiClients)
			if err != nil || !operatorsReady {
				return false, err
			}

			klog.V(amdgpuparams.AMDGPULogLevel).Info("✅ Cluster nodes and operators are stable.")

			return true, nil
		},
	)
}

// checkNodesReady checks if all nodes in the cluster are ready.
func checkNodesReady(apiClients *clients.Settings) (bool, error) {
	nodeList, err := nodes.List(apiClients, metav1.ListOptions{})
	if err != nil {
		if isConnectionError(err) {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof("API unreachable (nodes), retrying: %v", err)

			return false, nil
		}

		klog.Errorf("failed to list nodes: %v", err)

		return false, err
	}

	for _, node := range nodeList {
		ready, err := node.IsReady()
		if err != nil || !ready {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof("Node %s is not ready", node.Object.Name)

			return false, nil
		}
	}

	return true, nil
}

// checkOperatorsReady checks if all ClusterOperators are stable.
func checkOperatorsReady(ctx context.Context, apiClients *clients.Settings) (bool, error) {
	configClient, err := configclient.NewForConfig(apiClients.Config)
	if err != nil {
		klog.Errorf("failed to create config client: %v", err)

		return false, err
	}

	coList, err := configClient.ConfigV1().ClusterOperators().List(ctx, metav1.ListOptions{})
	if err != nil {
		if isConnectionError(err) {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof("API unreachable (operators), retrying: %v", err)

			return false, nil
		}

		klog.Errorf("failed to list cluster operators: %v", err)

		return false, err
	}

	for _, clusterOp := range coList.Items {
		if !isOperatorStable(&clusterOp) {
			return false, nil
		}
	}

	return true, nil
}

// isOperatorStable returns true if a ClusterOperator is Available and not Degraded.
func isOperatorStable(clusterOp *configv1.ClusterOperator) bool {
	isAvailable := false
	isDegraded := false

	for _, cond := range clusterOp.Status.Conditions {
		if cond.Type == configv1.OperatorAvailable && cond.Status == configv1.ConditionTrue {
			isAvailable = true
		}

		if cond.Type == configv1.OperatorDegraded && cond.Status == configv1.ConditionTrue {
			isDegraded = true
		}
	}

	// We ignore Progressing state to avoid flakes during background updates
	if !isAvailable || isDegraded {
		klog.V(amdgpuparams.AMDGPULogLevel).Infof(
			"ClusterOperator %s unstable (Available: %t, Degraded: %t)",
			clusterOp.Name, isAvailable, isDegraded)

		return false
	}

	return true
}

// isConnectionError checks if the error is a connection-related error.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	connectionPatterns := []string{
		"connection refused", "connection reset", "no such host", "i/o timeout",
		"network is unreachable", "eof", "context deadline exceeded",
		"tls handshake timeout", "dial tcp", "connect: connection timed out",
	}

	for _, pattern := range connectionPatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	return false
}

// WaitForPodRunningResilient waits for a pod to be in Running state with resilient retry logic.
func WaitForPodRunningResilient(podBuilder *pod.Builder, timeout time.Duration, isSNO bool) error {
	if isSNO {
		klog.V(amdgpuparams.AMDGPULogLevel).Info("SNO environment: using extended timeout for pod running check")

		if timeout < amdgpuparams.SNOPodRunningTimeout {
			timeout = amdgpuparams.SNOPodRunningTimeout
		}
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Infof("Waiting for pod %s to be running...", podBuilder.Object.Name)

	return wait.PollUntilContextTimeout(
		context.Background(),
		amdgpuparams.ConnectionRetryInterval,
		timeout,
		true,
		func(ctx context.Context) (bool, error) {
			// Use a short sub-timeout for the individual check
			err := podBuilder.WaitUntilRunning(1 * time.Minute)
			if err == nil {
				return true, nil
			}

			if isConnectionError(err) {
				klog.V(amdgpuparams.AMDGPULogLevel).Infof(
					"API unreachable waiting for pod %s (likely rebooting), retrying...",
					podBuilder.Object.Name)

				return false, nil
			}

			klog.Errorf("pod %s not running yet: %v", podBuilder.Object.Name, err)

			return false, nil
		},
	)
}

// WaitForPodsRunningResilient waits for multiple pods to be in Running state with resilient retry logic.
func WaitForPodsRunningResilient(apiClient *clients.Settings, podBuilders []*pod.Builder, isSNO bool) error {
	if len(podBuilders) == 0 {
		return fmt.Errorf("no pods provided to wait for")
	}

	// For SNO, first wait for cluster stability since node may have rebooted
	if isSNO {
		klog.V(amdgpuparams.AMDGPULogLevel).Info("SNO environment: waiting for cluster stability before checking pods")
		// We ignore the error here intentionally to allow checking pods even if one operator is flaky
		_ = WaitForClusterStability(apiClient, amdgpuparams.SNOClusterStabilityTimeout)
	}

	for _, podBuilder := range podBuilders {
		timeout := amdgpuparams.DefaultTimeout
		if isSNO {
			timeout = amdgpuparams.SNOPodRunningTimeout
		}

		if err := WaitForPodRunningResilient(podBuilder, timeout, isSNO); err != nil {
			return fmt.Errorf("failed waiting for pod %s: %w", podBuilder.Object.Name, err)
		}
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Info("All pods are now running")

	return nil
}

// WaitForClusterStabilityAfterNodeLabeller waits for cluster stability after Node Labeller is enabled.
func WaitForClusterStabilityAfterNodeLabeller(apiClient *clients.Settings, isSNO bool) error {
	timeout := amdgpuparams.ClusterStabilityTimeout
	if isSNO {
		timeout = amdgpuparams.SNOClusterStabilityTimeout
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Info("Waiting for cluster stability after enabling Node Labeller")

	return WaitForClusterStability(apiClient, timeout)
}

// WaitForAMDGPUDriverReady waits for the AMD GPU driver to be built and loaded by KMM.
func WaitForAMDGPUDriverReady(apiClient *clients.Settings, isSNO bool) error {
	timeout := amdgpuparams.ClusterStabilityTimeout
	if isSNO {
		timeout = amdgpuparams.SNOClusterStabilityTimeout
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Infof("Waiting for AMD GPU driver to be ready (timeout: %v)...", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := waitForKMMBuildPodsComplete(ctx, apiClient); err != nil {
		return fmt.Errorf("KMM build pods did not complete: %w", err)
	}

	if err := waitForDriverContainerPods(ctx, apiClient, isSNO); err != nil {
		return fmt.Errorf("driver-container pods not ready: %w", err)
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Info("AMD GPU driver is ready")

	return nil
}

// waitForKMMBuildPodsComplete waits for KMM build pods to complete (succeed or no longer exist).
func waitForKMMBuildPodsComplete(ctx context.Context, apiClient *clients.Settings) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Info("Checking for KMM build pods...")

	return wait.PollUntilContextCancel(ctx, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		podList, err := apiClient.CoreV1Interface.Pods(amdgpuparams.AMDGPUNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if isConnectionError(err) {
				return false, nil
			}

			klog.Errorf("failed to list KMM build pods in namespace %s: %v", amdgpuparams.AMDGPUNamespace, err)

			return false, err
		}

		buildPodsFound := false
		buildPodsCompleted := true

		for i := range podList.Items {
			podItem := &podList.Items[i]
			if strings.Contains(podItem.Name, "build") {
				buildPodsFound = true

				klog.V(amdgpuparams.AMDGPULogLevel).Infof(
					"Found build pod: %s, phase: %s", podItem.Name, podItem.Status.Phase)

				switch podItem.Status.Phase {
				case corev1.PodRunning, corev1.PodPending:
					buildPodsCompleted = false
				case corev1.PodFailed, corev1.PodUnknown:
					return false, fmt.Errorf("build pod %s failed", podItem.Name)
				case corev1.PodSucceeded:
					buildPodsCompleted = true
				}
			}
		}

		if !buildPodsFound {
			klog.V(amdgpuparams.AMDGPULogLevel).Info("No build pods found - using pre-built image")

			return true, nil
		}

		if buildPodsCompleted {
			klog.V(amdgpuparams.AMDGPULogLevel).Info(" All KMM build pods completed successfully")

			return true, nil
		}

		return false, nil
	})
}

// waitForDriverContainerPods waits for device-plugin pods to be running on AMD GPU nodes.
func waitForDriverContainerPods(ctx context.Context, apiClient *clients.Settings, _ bool) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Info("Waiting for device-plugin pods to be running...")

	return wait.PollUntilContextCancel(ctx, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		podList, err := apiClient.CoreV1Interface.Pods(amdgpuparams.AMDGPUNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if isConnectionError(err) {
				return false, nil
			}

			klog.Errorf("failed to list device-plugin pods in namespace %s: %v", amdgpuparams.AMDGPUNamespace, err)

			return false, err
		}

		status := checkDevicePluginPods(podList)

		if status.found && status.allRunning && status.count > 0 {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof(
				"Driver is ready - %d device-plugin/node-labeller pods running", status.count)

			return true, nil
		}

		if !status.found {
			klog.V(amdgpuparams.AMDGPULogLevel).Info("No device-plugin pods found yet, waiting...")
		}

		return false, nil
	})
}

// VerifyGPUHardwareReady checks if the AMD GPU hardware is properly initialized.
// It uses a polling mechanism to handle the latency between pod startup and node capacity updates.
func VerifyGPUHardwareReady(apiClient *clients.Settings, nodeName string) error {
	// 5 minute timeout for capacity to appear
	timeout := 5 * time.Minute
	klog.V(amdgpuparams.AMDGPULogLevel).Infof("Verifying GPU hardware on node %s (timeout: %v)", nodeName, timeout)

	return wait.PollUntilContextTimeout(
		context.Background(),
		10*time.Second,
		timeout,
		true,
		func(ctx context.Context) (bool, error) {
			node, err := apiClient.CoreV1Interface.Nodes().Get(ctx, nodeName, metav1.GetOptions{})
			if err != nil {
				if isConnectionError(err) {
					return false, nil
				}

				klog.Errorf("failed to get node %s: %v", nodeName, err)

				return false, err
			}

			gpuCapacity, exists := node.Status.Capacity["amd.com/gpu"]
			if !exists || gpuCapacity.Value() == 0 {
				klog.V(amdgpuparams.AMDGPULogLevel).Infof("Node %s: amd.com/gpu capacity not yet updated", nodeName)

				return false, nil
			}

			klog.V(amdgpuparams.AMDGPULogLevel).Infof(
				"Node %s has %d AMD GPU(s) available", nodeName, gpuCapacity.Value())

			return true, nil
		},
	)
}

// WaitForNodeLabellerDriverInit waits for the Node Labeller's driver-init container to complete.
func WaitForNodeLabellerDriverInit(apiClient *clients.Settings, timeout time.Duration) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Infof(
		"Waiting for Node Labeller driver-init containers to complete (timeout: %v)...", timeout)

	return wait.PollUntilContextTimeout(
		context.Background(),
		30*time.Second,
		timeout,
		true,
		func(ctx context.Context) (bool, error) {
			podList, err := apiClient.CoreV1Interface.Pods(amdgpuparams.AMDGPUNamespace).List(
				ctx, metav1.ListOptions{})
			if err != nil {
				if isConnectionError(err) {
					return false, nil
				}

				klog.Errorf("failed to list node labeller pods in namespace %s: %v", amdgpuparams.AMDGPUNamespace, err)

				return false, err
			}

			return evaluateNodeLabellerPods(podList)
		},
	)
}

// evaluateNodeLabellerPods iterates through pods and checks their driver-init status.
func evaluateNodeLabellerPods(podList *corev1.PodList) (bool, error) {
	allInitContainersReady := true
	nodeLabellerFound := false

	for i := range podList.Items {
		podItem := &podList.Items[i]

		if !strings.Contains(podItem.Name, "node-labeller") || strings.Contains(podItem.Name, "build") {
			continue
		}

		nodeLabellerFound = true

		ready, err := checkPodDriverInit(podItem)
		if err != nil {
			return false, err
		}

		if !ready {
			allInitContainersReady = false
		}
	}

	if !nodeLabellerFound {
		klog.V(amdgpuparams.AMDGPULogLevel).Info("No node-labeller pods found yet, waiting...")

		return false, nil
	}

	if allInitContainersReady {
		klog.V(amdgpuparams.AMDGPULogLevel).Info("✅ All Node Labeller driver-init containers completed")
		// Give a small grace period for labels to actually apply
		time.Sleep(10 * time.Second)

		return true, nil
	}

	return false, nil
}

// checkPodDriverInit checks the driver-init container status for a pod.
func checkPodDriverInit(podItem *corev1.Pod) (bool, error) {
	for _, initStatus := range podItem.Status.InitContainerStatuses {
		if strings.Contains(strings.ToLower(initStatus.Name), "driver-init") {
			continue
		}

		result := checkDriverInitContainer(initStatus, podItem.Name)
		if result.err != nil {
			return false, result.err
		}

		if !result.ready {
			return false, nil
		}
	}

	// Check if main container is running
	for _, containerStatus := range podItem.Status.ContainerStatuses {
		if containerStatus.Name == "node-labeller-container" && containerStatus.Ready {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof(
				"node-labeller-container is ready for pod %s", podItem.Name)
		}
	}

	return true, nil
}
