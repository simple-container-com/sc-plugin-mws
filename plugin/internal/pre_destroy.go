// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package echo

import (
	"context"
	"encoding/json"
	"fmt"

	scPlugin "github.com/simple-container-com/api/pkg/plugin"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
)

// PreDestroy is the echo stub for the PreDestroy RPC (Phase 1e,
// PreDestroyRequest v2). A real plugin's hook runs cloud-specific
// cleanup before the Pulumi destroy — mongodb drops databases named
// in service-user outputs; aws might drain an SQS queue; gcloud might
// release an Artifact Registry mirror.
//
// The echo stub narrates what crosses the wire so an operator reading
// the plugin's progress log can see what SC core actually packed. If
// stack_outputs or client_descriptor_json are empty, the plugin logs
// that too — it's a signal SC core was older than the plugin, which
// mongodb's live hook would key its skip-decision off.
//
// The three fields the sidecar packs are:
//
//   - req.StackOutputs — map[string][]byte, each value is JSON of
//     auto.OutputValue (`{"value": …, "secret": bool}`).
//   - req.ClientDescriptorJson — JSON of api.StackClientDescriptor for
//     stack.Client.Stacks[env]. Hooks read cloudExtras off this.
//   - req.DestroyParamsJson — JSON of api.DestroyParams. Carries env
//     name + dry-run flags.
func PreDestroy(ctx context.Context, publisher *scPlugin.Publisher, req *scpluginv1.PreDestroyRequest) error {
	publisher.Info("pre-destroy", fmt.Sprintf("resource_type=%q stack=%q env=%q",
		req.GetResourceType(), req.GetHeader().GetStackName(), req.GetHeader().GetEnvironment()))

	// Stack outputs: enumerate without decoding each value. A real hook
	// filters by key suffix (mongodb matches `-service-user`) before
	// unmarshalling.
	outputs := req.GetStackOutputs()
	publisher.Info("pre-destroy", fmt.Sprintf("stack outputs: %d key(s)", len(outputs)))
	for k, v := range outputs {
		publisher.Info("pre-destroy", fmt.Sprintf("  output %q = %s", k, truncateForLog(string(v))))
	}

	// Client descriptor: a real hook reads cloudExtras off this.
	if raw := req.GetClientDescriptorJson(); len(raw) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			publisher.Info("pre-destroy", fmt.Sprintf("client descriptor type=%v", decoded["type"]))
		}
	} else {
		publisher.Info("pre-destroy", "client descriptor was absent — older SC core, or deploy against an env with no config")
	}

	// Destroy params: env name + dry-run flags.
	if raw := req.GetDestroyParamsJson(); len(raw) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			publisher.Info("pre-destroy", fmt.Sprintf("destroy env=%v destroySecretsStack=%v",
				decoded["environment"], decoded["destroySecretsStack"]))
		}
	}

	return nil
}

// truncateForLog clips a value to a readable prefix. A real hook
// NEVER logs values — stack outputs can contain IAM keys. The echo
// stub does because it's a template, not a cloud driver, and
// template clarity wins over paranoia on strings it never sees.
func truncateForLog(s string) string {
	const limit = 80
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
