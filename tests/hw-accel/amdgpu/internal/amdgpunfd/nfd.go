package amdgpunfd

import (
	"fmt"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nfd"
	"github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/amdgpu/internal/amdgpucommon"
	amdgpuparams "github.com/rh-ecosystem-edge/eco-gotests/tests/hw-accel/amdgpu/params"
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

// DeleteAMDGPUFeatureRule deletes the AMD GPU NFD FeatureRule.
func DeleteAMDGPUFeatureRule(apiClient *clients.Settings) error {
	klog.V(amdgpuparams.AMDGPULogLevel).Info("Deleting AMD GPU FeatureRule")

	// FeatureRule will be cleaned up when NFD operator is uninstalled
	// This is a placeholder for explicit cleanup if needed
	klog.V(amdgpuparams.AMDGPULogLevel).Info("FeatureRule will be cleaned up with NFD operator uninstall")

	return nil
}
