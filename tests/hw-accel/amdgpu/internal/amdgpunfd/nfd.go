package amdgpunfd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nfd"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nodes"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/amdgpu/internal/amdgpucommon"
	amdgpuparams "github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/amdgpu/params"
	"github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/nfd/nfdparams"
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
            "namespace": "openshift-amd-gpu"
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
// It covers: NodeFeatureRule existence and spec, NFD pod statuses, and node NFD labels.
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

	// 2. Log NFD worker and controller pod statuses and their container states.
	nfdPods, err := pod.List(apiClient, nfdparams.NFDNamespace)
	if err != nil {
		klog.Errorf("Failed to list NFD pods in %s: %v", nfdparams.NFDNamespace, err)
	} else {
		klog.Errorf("NFD pods in %s:", nfdparams.NFDNamespace)
		for _, p := range nfdPods {
			klog.Errorf("  Pod: %s  Phase: %s  Ready: %v",
				p.Object.Name, p.Object.Status.Phase, isPodReady(p))
			for _, cs := range p.Object.Status.ContainerStatuses {
				klog.Errorf("    Container: %s  Ready: %v  RestartCount: %d  State: %+v",
					cs.Name, cs.Ready, cs.RestartCount, cs.State)
			}
		}
	}

	// 3. Log all node labels — both NFD feature labels and AMD GPU labels.
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
