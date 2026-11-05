// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"fmt"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/simple-container-com/api/pkg/api"
	pApi "github.com/simple-container-com/api/pkg/clouds/pulumi/api"
)

// EchoComputeProcessor is the canonical ComputeProcessor shape every
// real plugin's ComputeProcess body should wear. The main.go handler
// never calls this function directly — it passes it to
// runtime.RunComputeProcess, which owns the fn → drain → marshal
// sequence and hands the typed response back.
//
// Writing a ComputeProcessor:
//
//   - Everything you want to end up in the ComputeProcessResponse is
//     written via the shared pApi.ComputeContextCollector:
//     AddEnvVariableIfNotExist, AddSecretEnvVariableIfNotExist,
//     AddResourceTplExtension, AddDependencyTplExtension.
//
//   - Env mutations that depend on resolved resource outputs (bucket
//     name, user password, cluster connection string, …) run from
//     INSIDE an ApplyT callback on that output, and the output is
//     registered with the collector via collector.AddOutput(ctx, out)
//     so the runtime's drain blocks on it before marshalling the
//     response. The pattern below exercises that path — one direct env
//     mutation plus one ApplyT-wrapped secret mutation.
//
//   - DO NOT reimplement the collector in-plugin. The shared
//     runtime.Collector's AddOutput captures the Output; its drain is
//     package-private so a plugin author cannot forget to await.
//     Writing a local capturingCollector reintroduces trap 16 (silent
//     env-var drop; sc-plugin-aws S3 ComputeProcessor live-caught on
//     agent-native-pdf-rendering run f4355798, 2026-11-02). See
//     forge/docs/design/2026-11-01/plugin-extraction-playbook/ trap 16.
//
//   - Return (*api.ResourceOutput, error). The ResourceOutput.Ref is
//     opaque to the collector — SC core uses it to mint a response
//     URN. For an echo stub there is no cloud resource to reference;
//     return an sdk.StringOutput as a placeholder.
func EchoComputeProcessor(ctx *sdk.Context, _ api.Stack, input api.ResourceInput, collector pApi.ComputeContextCollector, _ pApi.ProvisionParams) (*api.ResourceOutput, error) {
	resName := input.Descriptor.Name
	resType := input.Descriptor.Type
	stackName := input.StackParams.StackName

	// Direct mutation (no ApplyT) — the simple case. A plugin writes
	// an env var whose value is already a plain string.
	collector.AddEnvVariableIfNotExist("ECHO_RESOURCE_NAME", resName, resType, resName, stackName)

	// ApplyT-wrapped mutation — the pattern that trap 16 silently
	// broke in sc-plugin-aws. A real plugin would resolve the value
	// off a resource's Output (bucket.ID(), user.Password, …); here
	// we synthesise one via sdk.String so the shape runs in tests.
	// The shared Collector's AddOutput captures the Output; the
	// runtime's drain blocks on resolution before response marshal.
	resolved := sdk.String("replace-me-with-real-cloud-output").ToStringOutput().ApplyT(func(value string) string {
		collector.AddSecretEnvVariableIfNotExist("ECHO_SECRET_TOKEN", value, resType, resName, stackName)
		return value
	}).(sdk.StringOutput)
	collector.AddOutput(ctx, resolved)

	collector.AddResourceTplExtension(resName, map[string]string{
		"echoResourceName": resName,
		"echoResourceType": resType,
	})
	collector.AddDependencyTplExtension("consumer", resName, map[string]string{
		"echoStackName": stackName,
	})

	return &api.ResourceOutput{Ref: sdk.String(fmt.Sprintf("echo-compute:%s:%s", resType, resName)).ToStringOutput()}, nil
}
