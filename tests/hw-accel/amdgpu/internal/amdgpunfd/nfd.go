package amdgpunfd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/events"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nfd"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nodes"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/rbac"
	"github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/amdgpu/internal/amdgpucommon"
	amdgpuparams "github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/amdgpu/params"
	"github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/nfd/nfdparams"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// CreateAMDGPUFeatureRule creates an NFD FeatureRule for advanced AMD GPU detection and labeling.
func CreateAMDGPUFeatureRule(apiClient *clients.Settings) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Infof("Creating NFD FeatureRule for enhanced AMD GPU detection")

	featureRuleBuilder := nfd.NewNodeFeatureRuleBuilderFromObjectString(apiClient, getAMDGPUFeatureRuleYAML())
	if featureRuleBuilder == nil {
		klog.Errorf("failed to create NodeFeatureRule builder")

		return fmt.Errorf("failed to create NodeFeatureRule builder")
	}

	if featureRuleBuilder.Exists() {
		klog.V(amdgpuparams.AMDGPULogLevel).Info("AMD GPU FeatureRule already exists")

		return nil
	}

	_, err := featureRuleBuilder.Create()
	if err != nil {
		return handleFeatureRuleCreationError(err)
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Info("Successfully created AMD GPU FeatureRule")
	klog.V(amdgpuparams.AMDGPULogLevel).Info("This will enhance AMD GPU node detection and labeling via NFD")

	// Pull back and log the created FeatureRule to confirm its namespace and rules.
	created, pullErr := nfd.PullFeatureRule(apiClient,
		featureRuleBuilder.Definition.Name,
		featureRuleBuilder.Definition.Namespace)
	if pullErr != nil {
		klog.Errorf("Failed to pull created NodeFeatureRule for verification: %v", pullErr)
	} else {
		specJSON, _ := json.Marshal(created.Object.Spec)
		klog.V(amdgpuparams.AMDGPULogLevel).Infof(
			"NodeFeatureRule verified: name=%s namespace=%s spec=%s",
			created.Object.Name, created.Object.Namespace, string(specJSON))

		if created.Object.Namespace != nfdparams.NFDNamespace {
			klog.Errorf("WARNING: NodeFeatureRule is in namespace '%s' but NFD watches '%s' — labels may not be applied",
				created.Object.Namespace, nfdparams.NFDNamespace)
		}
	}

	return nil
}

// getAMDGPUFeatureRuleYAML returns the YAML configuration for AMD GPU NodeFeatureRule.
func getAMDGPUFeatureRuleYAML() string {
	return `
[
    {
        "apiVersion": "nfd.openshift.io/v1alpha1",
        "kind": "NodeFeatureRule",
        "metadata": {
            "name": "amd-gpu-feature-rule",
            "namespace": "openshift-nfd"
        },
        "spec": {
            "rules": [
                {
                    "name": "amd.gpu.device",
                    "labels": {
                        "amd.com/gpu": "true",
                        "feature.node.kubernetes.io/amd-gpu": "true"
                    },
                    "matchFeatures": [
                        {
                            "feature": "pci.device",
                            "matchExpressions": {
                                "vendor": {
                                    "op": "In",
                                    "value": [
                                        "1002"
                                    ]
                                },
                                "device": {
                                    "op": "In",
                                    "value": [
                                        "75a3",
                                        "75a0",
                                        "74a5",
                                        "74a0",
                                        "74a1",
                                        "74a9",
                                        "74bd",
                                        "740f",
                                        "7408",
                                        "740c",
                                        "738c",
                                        "738e"
                                    ]
                                }
                            }
                        }
                    ]
                }
            ]
        }
    }
]
		`
}

// handleFeatureRuleCreationError handles errors during FeatureRule creation.
func handleFeatureRuleCreationError(err error) error {
	klog.Errorf("Error creating AMD GPU FeatureRule: %v", err)

	if amdgpucommon.IsCRDNotAvailable(err) {
		klog.Errorf("NFD FeatureRule CRD not available - manual creation required")

		return fmt.Errorf("NFD FeatureRule CRD not available, manual creation required")
	}

	return err
}

// LogNFDDiagnostics logs detailed NFD state to help debug why labels are not applied.
// It covers: NodeFeatureRule spec, NFD pod conditions/logs/events, and node NFD labels.
func LogNFDDiagnostics(apiClient *clients.Settings) {
	klog.Errorf("=== NFD DIAGNOSTICS START ===")

	// 1. Check NodeFeatureRule in both likely namespaces.
	for _, ns := range []string{amdgpuparams.AMDGPUNamespace, nfdparams.NFDNamespace} {
		rule, err := nfd.PullFeatureRule(apiClient, "amd-gpu-feature-rule", ns)
		if err != nil {
			klog.Errorf("NodeFeatureRule 'amd-gpu-feature-rule' NOT found in namespace %s: %v", ns, err)
		} else {
			specJSON, _ := json.Marshal(rule.Object.Spec)
			klog.Errorf("NodeFeatureRule found in namespace %s: spec=%s", ns, string(specJSON))
		}
	}

	// 2. Describe NFD pods: phase, conditions, container states, and last 50 log lines.
	nfdPods, err := pod.List(apiClient, nfdparams.NFDNamespace)
	if err != nil {
		klog.Errorf("Failed to list NFD pods in %s: %v", nfdparams.NFDNamespace, err)
	} else {
		klog.Errorf("NFD pods in %s (%d found):", nfdparams.NFDNamespace, len(nfdPods))

		for _, p := range nfdPods {
			klog.Errorf("  --- Pod: %s  Phase: %s  Ready: %v ---",
				p.Object.Name, p.Object.Status.Phase, isPodReady(p))

			// Pod conditions (surface Unschedulable, PodHasNetwork, etc.)
			for _, cond := range p.Object.Status.Conditions {
				klog.Errorf("    Condition: type=%s status=%s reason=%s message=%s",
					cond.Type, cond.Status, cond.Reason, cond.Message)
			}

			// Container statuses + last 50 log lines per container.
			for _, cs := range p.Object.Status.ContainerStatuses {
				klog.Errorf("    Container: %s  Ready: %v  RestartCount: %d  State: %+v",
					cs.Name, cs.Ready, cs.RestartCount, cs.State)
				logNFDContainerLogs(p, cs.Name)
			}

			// Init container statuses.
			for _, cs := range p.Object.Status.InitContainerStatuses {
				klog.Errorf("    InitContainer: %s  Ready: %v  RestartCount: %d  State: %+v",
					cs.Name, cs.Ready, cs.RestartCount, cs.State)
			}
		}
	}

	// 3. Log Warning events in the NFD namespace (catches SCC denials, image pull errors, etc.)
	nfdEvents, evErr := events.List(apiClient, nfdparams.NFDNamespace)
	if evErr != nil {
		klog.Errorf("Failed to list events in %s: %v", nfdparams.NFDNamespace, evErr)
	} else {
		klog.Errorf("Events in %s:", nfdparams.NFDNamespace)

		for _, ev := range nfdEvents {
			if ev.Object.Type == "Warning" {
				klog.Errorf("  WARNING event: reason=%s obj=%s/%s count=%d message=%s",
					ev.Object.Reason,
					ev.Object.InvolvedObject.Kind, ev.Object.InvolvedObject.Name,
					ev.Object.Count, ev.Object.Message)
			}
		}
	}

	// 4. Log all node labels — both NFD feature labels and AMD GPU labels.
	allNodes, err := nodes.List(apiClient, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("Failed to list nodes: %v", err)
	} else {
		for _, n := range allNodes {
			var nfdLabels, amdLabels []string

			for k, v := range n.Object.Labels {
				switch {
				case strings.HasPrefix(k, "feature.node.kubernetes.io/"):
					nfdLabels = append(nfdLabels, fmt.Sprintf("%s=%s", k, v))
				case strings.HasPrefix(k, "amd.com/"):
					amdLabels = append(amdLabels, fmt.Sprintf("%s=%s", k, v))
				}
			}

			klog.Errorf("Node %s — NFD labels: %v | AMD labels: %v",
				n.Object.Name, nfdLabels, amdLabels)
		}
	}

	klog.Errorf("=== NFD DIAGNOSTICS END ===")
}

// logNFDContainerLogs fetches the last 50 lines of logs from a container and logs them.
func logNFDContainerLogs(p *pod.Builder, containerName string) {
	const tailLines = int64(50)

	logBytes, err := p.GetLogsWithOptions(&corev1.PodLogOptions{
		Container: containerName,
		TailLines: &[]int64{tailLines}[0],
	})
	if err != nil {
		klog.Errorf("      [logs] failed to get logs for container %s: %v", containerName, err)

		return
	}

	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	klog.Errorf("      [logs] last %d lines for container %s:", len(lines), containerName)

	for _, line := range lines {
		klog.Errorf("        %s", line)
	}
}

// isPodReady returns true if all containers in the pod are ready.
func isPodReady(p *pod.Builder) bool {
	for _, cs := range p.Object.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}

	return len(p.Object.Status.ContainerStatuses) > 0
}

// DeleteAMDGPUFeatureRule deletes the AMD GPU NFD FeatureRule.
func DeleteAMDGPUFeatureRule(apiClient *clients.Settings) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Info("Deleting AMD GPU FeatureRule")

	// FeatureRule will be cleaned up when NFD operator is uninstalled
	// This is a placeholder for explicit cleanup if needed
	klog.V(amdgpuparams.AMDGPULogLevel).Info("FeatureRule will be cleaned up with NFD operator uninstall")

	return nil
}

// GrantNFDWorkerPrivilegedSCC grants the privileged SCC to the nfd-worker ServiceAccount.
// This is needed because the NFD operator (nfd.4.18.0-202602132343) has a bug where its
// controller-manager fails to reconcile the SCC for nfd-worker, preventing the DaemonSet
// from starting. We work around it by creating the ClusterRoleBinding manually.
func GrantNFDWorkerPrivilegedSCC(apiClient *clients.Settings) error {
	const crbName = "nfd-worker-privileged-scc"

	klog.V(amdgpuparams.AMDGPULogLevel).Infof(
		"Granting privileged SCC to nfd-worker ServiceAccount in %s", nfdparams.NFDNamespace)

	subject := rbacv1.Subject{
		Kind:      "ServiceAccount",
		Name:      "nfd-worker",
		Namespace: nfdparams.NFDNamespace,
	}

	crbBuilder := rbac.NewClusterRoleBindingBuilder(
		apiClient,
		crbName,
		"system:openshift:scc:privileged",
		subject,
	)

	if crbBuilder.Exists() {
		klog.V(amdgpuparams.AMDGPULogLevel).Infof("ClusterRoleBinding %s already exists", crbName)

		return nil
	}

	_, err := crbBuilder.Create()
	if err != nil {
		return fmt.Errorf("failed to create ClusterRoleBinding %s: %w", crbName, err)
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Infof(
		"Successfully granted privileged SCC to nfd-worker in %s", nfdparams.NFDNamespace)

	return nil
}

// RecoverNFDWorkerPodsIfStuck waits briefly for nfd-worker pods to appear, then deletes any
// that are stuck in CreateContainerConfigError so they restart with the SCC binding in place.
// It also re-grants the SCC before deleting, in case the NFD operator deleted our CRB during reconcile.
func RecoverNFDWorkerPodsIfStuck(apiClient *clients.Settings) error {
	const waitForPods = 30 * time.Second

	klog.V(amdgpuparams.AMDGPULogLevel).Infof(
		"Waiting %s for nfd-worker pods to appear before checking for stuck state", waitForPods)
	time.Sleep(waitForPods)

	nfdWorkerPods, err := pod.List(apiClient, nfdparams.NFDNamespace,
		metav1.ListOptions{LabelSelector: "app=nfd-worker"})
	if err != nil {
		return fmt.Errorf("failed to list nfd-worker pods: %w", err)
	}

	stuck := 0

	for _, p := range nfdWorkerPods {
		if isStuckInCreateContainerConfigError(p) {
			stuck++
		}
	}

	if stuck == 0 {
		klog.V(amdgpuparams.AMDGPULogLevel).Infof("No nfd-worker pods stuck in CreateContainerConfigError")

		return nil
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Infof(
		"%d nfd-worker pod(s) stuck in CreateContainerConfigError — re-granting SCC and deleting stuck pods", stuck)

	// Re-grant in case the NFD operator reconciler deleted our CRB in the meantime.
	if err := GrantNFDWorkerPrivilegedSCC(apiClient); err != nil {
		return fmt.Errorf("failed to re-grant privileged SCC before pod recovery: %w", err)
	}

	for _, p := range nfdWorkerPods {
		if !isStuckInCreateContainerConfigError(p) {
			continue
		}

		klog.V(amdgpuparams.AMDGPULogLevel).Infof("Deleting stuck nfd-worker pod %s", p.Object.Name)

		_, err := p.Delete()
		if err != nil {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof("Failed to delete pod %s: %v", p.Object.Name, err)
		}
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Infof(
		"Deleted %d stuck nfd-worker pod(s); they will restart with the privileged SCC binding", stuck)

	return nil
}

// isStuckInCreateContainerConfigError returns true if any container in the pod is waiting
// with reason CreateContainerConfigError (typically caused by missing SCC permissions).
func isStuckInCreateContainerConfigError(p *pod.Builder) bool {
	for _, cs := range p.Object.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CreateContainerConfigError" {
			return true
		}
	}

	return false
}

// RevokeNFDWorkerPrivilegedSCC removes the privileged SCC ClusterRoleBinding for nfd-worker.
func RevokeNFDWorkerPrivilegedSCC(apiClient *clients.Settings) error {
	const crbName = "nfd-worker-privileged-scc"

	klog.V(amdgpuparams.AMDGPULogLevel).Infof("Revoking privileged SCC ClusterRoleBinding %s", crbName)

	crbBuilder, err := rbac.PullClusterRoleBinding(apiClient, crbName)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			klog.V(amdgpuparams.AMDGPULogLevel).Infof("ClusterRoleBinding %s not found, nothing to revoke", crbName)

			return nil
		}

		return fmt.Errorf("failed to pull ClusterRoleBinding %s: %w", crbName, err)
	}

	err = crbBuilder.Delete()
	if err != nil {
		return fmt.Errorf("failed to delete ClusterRoleBinding %s: %w", crbName, err)
	}

	klog.V(amdgpuparams.AMDGPULogLevel).Infof("Successfully revoked privileged SCC ClusterRoleBinding %s", crbName)

	return nil
}
