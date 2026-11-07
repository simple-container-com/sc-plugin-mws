// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/pulumi/pulumi-tls/sdk/v5/go/tls"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/simple-container-com/api/pkg/api"
	"github.com/simple-container-com/api/pkg/clouds/vm"
	scPlugin "github.com/simple-container-com/api/pkg/plugin"
	scpluginv1 "github.com/simple-container-com/api/pkg/plugin/proto/v1"
	mwscompute "github.com/simple-container-com/pulumi-mws/sdk/go/mws/compute"
	mwsvpc "github.com/simple-container-com/pulumi-mws/sdk/go/mws/vpc"

	mwsApi "github.com/simple-container-com/sc-plugin-mws/api"
)

// Fixed pieces of the shape every machine gets. Each is a measurement or a
// vendor-example value, not a preference — see the comment on its use.
const (
	// defaultSubnetCIDR is the private range the plugin creates when the operator
	// declared no `network:`. /16 and 192.168 both come from the vendor's own
	// examples (the docs example and both go-sdk VM examples use exactly this),
	// so it is the range MWS is known to accept.
	defaultSubnetCIDR = "192.168.0.0/16"

	// defaultMTU is MWS's own default (1500, from `mws_vpc_network`'s schema). Set
	// explicitly so it appears in the Pulumi diff rather than materialising from a
	// cloud-side default that could move.
	defaultMTU = 1500

	// gracefulShutdownTimeout is the vendor example's value verbatim. The format is
	// a duration STRING with a space (`1m 30s`), not Go's `1m30s`, and not seconds.
	gracefulShutdownTimeout = "1m 30s"

	// powerOn is the `hardware.power` value that means "run it". Spelled out rather
	// than taken from a generated enum because the bridge generates none for it.
	powerOn = "ON"

	// bootDiskName is the `storage.disks[].name` of the root disk. "boot" is the
	// vendor's; it is a per-VM label, not a cloud-wide object name.
	bootDiskName = "boot"

	// Firewall-rule constants, all from `createFirewallRuleAllowSSH` in
	// `go-sdk@v0.29.0/examples/vm_ssh_test.go` — the one piece of vendor code that
	// creates a rule and then successfully SSHes through it.
	firewallDirectionIngress = "INGRESS"
	firewallActionAllow      = "ALLOW"
	// 1000 is the HIGHEST priority MWS permits: the vendor's comment is "the lower
	// the number, the higher the priority. Allowed values are 1000-64535", so 1000
	// is the floor of the range and not a midpoint someone picked.
	firewallPriority = 1000
	// anyIPv4 is the source for a PUBLIC hole. An internal one gets
	// defaultSubnetCIDR instead — see sourceCIDRFor; do not reach for this
	// constant directly in the rule loop.
	anyIPv4 = "0.0.0.0/0"
)

// maxMwsNameLen bounds a cloud-side object name.
//
// 63 is the Kubernetes DNS-label limit, chosen because MWS models every resource
// Kubernetes-style (`kind`/`metadata`/`spec`) and every vendor example name is a
// lowercase DNS label. **The real limit is UNMEASURED** — the provider schema
// declares no validator and no error was ever provoked — so this is a safe guess
// in the direction that cannot break a short name. If a long resource name ever
// gets refused, this constant is the first thing to look at.
const maxMwsNameLen = 63

// VmHosts converges one MWS virtual machine for a `type: vm` resource whose
// `provider:` is mws-vm.
//
// # The shape, and why it is six resources rather than two
//
//	vpc.Network          internet_access=true, mtu=1500
//	  └─ vpc.Subnet      192.168.0.0/16 in the resolved region
//	       └─ vpc.Address        the machine's INTERNAL address, reserved up-front
//	vpc.ExternalAddress  the public address, in the resolved region
//	compute.VirtualMachine   binds both addresses through one_to_one_nat
//	vpc.FirewallRule     one per declared ingress port
//
// Two of those deserve an explanation, because a shorter version of this function
// is possible and is wrong:
//
//   - **The internal address is a standalone `vpc.Address`, not an inline
//     `address.spec.subnet`.** The go-sdk example uses the inline form and it
//     works; the Terraform provider's own docs example uses the standalone form.
//     The standalone form is used here because the FIREWALL RULE NEEDS THE
//     INTERNAL IP: MWS evaluates ingress rules after the one-to-one NAT
//     translation, so the destination CIDR is the machine's private address. With
//     the inline form that address only exists inside
//     `virtualMachine.status.network.network_interfaces[].addresses[].ip_address`
//     — a nested array output populated after the machine boots — and a firewall
//     rule that has to wait for that is a rule that does not exist while cloud-init
//     is still running. Reserving the address first makes the IP a first-class
//     output of a resource that converges in seconds.
//
//   - **The boot disk is INLINE (`disk.spec`), not a separate `compute.Disk` with
//     `disk.ref`.** The docs example uses the separate resource; the go-sdk
//     example uses the inline spec. Inline is used here for lifecycle reasons, not
//     brevity: a separate disk resource is independently destroyable, and nothing
//     in SC's model names it, so an operator who deletes the vm resource would be
//     left with an orphaned paid disk. Inline ties the disk's life to the
//     machine's, which is what "the root disk" means.
//
// # The three things that make this cloud different from the fleet's others
//
//  1. **Ingress is default-DENY per network**, so a machine with no firewall rule
//     is a machine that cannot be reached at all — including by SC's own deploy.
//     The rules below are therefore load-bearing for the deploy itself, not just
//     for the workload. An `ingressPorts` list that omits 22 would lock SC out;
//     core's own validation requires the SSH port, which is why this function does
//     not re-check it.
//
//  2. **Cloud-init works, and this is its FIRST deploy-path caller anywhere in
//     SC.** `vm.RenderCloudInit` has unit tests against its output string and, as
//     of 2026-11-06, zero callers that ever reached a cloud. So the host-key pin
//     below rests on a template that has never been executed by a real cloud-init.
//     If the first deploy cannot SSH in, read
//     `/var/log/cloud-init-output.log` on the console before suspecting anything
//     in this file — an ed25519 host-key install failure there presents
//     identically to a wrong address.
//
//  3. **The host key IS pinned.** MWS accepts user-data, so SC generates the
//     machine's own ed25519 host key, seeds it at first boot and verifies it on
//     connect. That is the strong form — unlike Cloud.ru Evolution, which refuses
//     user-data and must trust the first connection. `cfg.HostKey` is accepted as
//     an override for a machine whose key the operator already knows, but there is
//     no trust-on-first-use path here and there should not be one.
//
// Facts and provenance: ../../CLOUD-FACTS.md. Design:
// forge/docs/design/2026-10-13/sc-russia-market-readiness/ and
// docs/design/2026-10-24/sc-plugin-architecture/07-plugin-first-for-new-clouds.md §3.
func VmHosts(pctx *sdk.Context, req *scpluginv1.VmHostsRequest) (*scpluginv1.VmHostsResponse, error) {
	resourceName := req.GetResourceName()
	hostName := fmt.Sprintf("%s-host-0", resourceName)
	env := req.GetHeader().GetEnvironment()

	var cfg vm.VmConfig
	if err := json.Unmarshal(req.GetConfigJson(), &cfg); err != nil {
		return nil, errors.Wrapf(err, "decode VmConfig for vm %q", resourceName)
	}
	ports := portsFromProto(req.GetPorts())

	// Trap 19's first caller in this repo: ConvertAuth is what turns the
	// `credentials:` reference into a typed account config, and Validate is what
	// turns a missing field into a sentence instead of a cloud-side 401 twenty
	// minutes later. Both, in this order, before any resource is registered.
	var account mwsApi.AccountConfig
	if err := api.ConvertAuth(&cfg, &account); err != nil {
		return nil, errors.Wrapf(err,
			"failed to read MWS credentials of vm %q; `credentials:` must reference an `auth:` entry of "+
				"type %q", resourceName, mwsApi.AuthTypeMwsServiceAccount)
	}
	if err := account.Validate(); err != nil {
		return nil, errors.Wrapf(err, "invalid MWS credentials of vm %q", resourceName)
	}

	zone, err := resolveZone(&cfg, account.EffectiveZone())
	if err != nil {
		return nil, errors.Wrapf(err, "cannot place vm %q", resourceName)
	}
	region := regionOfZone(zone)

	// Every mapping decision is made HERE, before a single resource is registered,
	// so a typo in `size:` or `image:` is an error on the config rather than a
	// half-built network in the operator's project.
	vmType, err := vmTypeRef(cfg.EffectiveSize())
	if err != nil {
		return nil, errors.Wrapf(err, "cannot size vm %q", resourceName)
	}
	diskType, err := diskTypeRef(cfg.EffectiveSize())
	if err != nil {
		return nil, errors.Wrapf(err, "cannot size the boot disk of vm %q", resourceName)
	}
	diskBytes, err := diskSizeBytes(cfg.EffectiveSize())
	if err != nil {
		return nil, errors.Wrapf(err, "cannot size the boot disk of vm %q", resourceName)
	}
	bootImage, err := imageRef(cfg.EffectiveImage())
	if err != nil {
		return nil, errors.Wrapf(err, "cannot resolve the boot image of vm %q", resourceName)
	}

	// The pinned-network path is refused rather than half-honoured, and the reason
	// is a reference-shape mismatch that no amount of care in this file can bridge.
	// SC's `network:` block carries an `id` plus a zone→subnet-**id** map. Every
	// MWS field that points at a network — `subnet.network`, `address.network`,
	// `firewallRule.network` — takes the network's **NAME**, and so does the only
	// lookup upstream exposes (`LookupNetworkArgs.Network` is a name too), so there
	// is no id→name resolution available even as a data source. Accepting the block
	// and passing `cfg.Network.ID` through would send an id where a name is
	// expected and surface as a 4xx naming neither.
	if cfg.Network != nil {
		return nil, errors.Errorf(
			"`network` of vm %q is not supported with `provider: %s` yet: this driver creates the network, "+
				"subnet and both addresses itself, and MWS references a network by NAME while SC's "+
				"`network:` block carries an `id` — upstream exposes no id-to-name lookup, so the id "+
				"cannot be resolved. Remove `network:`, or place the machine by hand and declare it with "+
				"`provider: %s` and `hosts:`",
			resourceName, vm.DriverMws, vm.DriverByo,
		)
	}

	base := mwsObjectName(toResName(resourceName, env), "")
	provider, err := Provider(pctx, toResName(resourceName+"-mws", env), zone, &account)
	if err != nil {
		return nil, err
	}
	opts := []sdk.ResourceOption{sdk.Provider(provider)}

	// Two key pairs, both STACK RESOURCES rather than operator secrets, both
	// ed25519 (the only algorithm whose public half cloud-init's
	// `ssh_keys: ed25519_*` slots accept):
	//
	//   - the DEPLOY key, whose public half becomes the machine's authorized_keys
	//     and whose private half core connects with;
	//   - the HOST key, whose private half is written to /etc/ssh by cloud-init and
	//     whose public half is pinned on the connection.
	//
	// Generated by tls.PrivateKey and not in Go, because a key minted per call
	// would change the user-data on every deploy — and user-data is
	// replace-on-change, so the machine (and its boot disk, and everything deployed
	// onto it) would be rebuilt every time. The checkpoint is what makes them stable.
	deployKey, err := tls.NewPrivateKey(pctx, toResName(resourceName+"-deploy-key", env), &tls.PrivateKeyArgs{
		Algorithm: sdk.String("ED25519"),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create deploy key for vm %q", resourceName)
	}
	hostKey, err := tls.NewPrivateKey(pctx, toResName(resourceName+"-host-key", env), &tls.PrivateKeyArgs{
		Algorithm: sdk.String("ED25519"),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create host key for vm %q", resourceName)
	}

	// The user-data, assembled inside an ApplyT because all three key halves are
	// outputs. Secret-wrapped on the way out: it carries the host PRIVATE key, and
	// an unmarked resource input is rendered verbatim in Pulumi's diff and stored
	// unencrypted in the checkpoint.
	userData := sdk.All(
		deployKey.PublicKeyOpenssh,
		hostKey.PrivateKeyOpenssh,
		hostKey.PublicKeyOpenssh,
	).ApplyT(func(vs []any) (string, error) {
		rendered, err := vm.RenderCloudInit(&cfg, vm.RoleCompose, vm.HostKeys{
			DeployPublicKey: vs[0].(string),
			HostPrivateKey:  vs[1].(string),
			HostPublicKey:   vs[2].(string),
		})
		if err != nil {
			return "", errors.Wrapf(err, "failed to render cloud-init for vm %q", resourceName)
		}
		return rendered, nil
	}).(sdk.StringOutput)

	network, err := mwsvpc.NewNetwork(pctx, toResName(resourceName+"-net", env), &mwsvpc.NetworkArgs{
		Network: sdk.String(mwsObjectName(base, "net")),
		Project: sdk.StringPtr(account.Project),
		// Required, and not merely convenient: SC's cloud-init installs docker from
		// get.docker.com and pulls the stack's images. A network without it produces
		// a machine that boots, answers SSH, and has no docker — which presents as a
		// compose preflight failure rather than as a networking problem.
		InternetAccess: sdk.BoolPtr(true),
		Mtu:            sdk.IntPtr(defaultMTU),
	}, opts...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create network for vm %q", resourceName)
	}

	subnet, err := mwsvpc.NewSubnet(pctx, toResName(resourceName+"-subnet", env), &mwsvpc.SubnetArgs{
		Subnet:  sdk.String(mwsObjectName(base, "subnet")),
		Project: sdk.StringPtr(account.Project),
		// The network's NAME, not its id — `mws_vpc_subnet.network` is the one
		// cross-resource reference in this whole file that takes a name. Taken off
		// the created resource rather than re-derived from mwsObjectName so the
		// dependency edge is real and the subnet waits for the network.
		Network: network.Network,
		Cidr:    sdk.String(defaultSubnetCIDR),
		// A subnet is regional even though the provider is zonal. This is the field
		// that makes `region:` a real part of the config rather than decoration.
		Region: sdk.StringPtr(region),
	}, opts...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create subnet for vm %q", resourceName)
	}

	internalAddress, err := mwsvpc.NewAddress(pctx, toResName(resourceName+"-addr", env), &mwsvpc.AddressArgs{
		Address: sdk.String(mwsObjectName(base, "addr")),
		Project: sdk.StringPtr(account.Project),
		Network: network.Network,
		// The subnet's `metadata.id`, NOT its name — the asymmetry with the line
		// above is MWS's, not a slip. `Metadata.Id()` is a StringPtrOutput because
		// the field is optional in the schema; requireString turns an absent value
		// into an error naming the field instead of a silent "".
		Subnet: requireString(pctx, subnet.Metadata.Id(), "subnet metadata.id"),
	}, opts...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to reserve the internal address of vm %q", resourceName)
	}

	externalAddress, err := mwsvpc.NewExternalAddress(pctx, toResName(resourceName+"-ext-addr", env),
		&mwsvpc.ExternalAddressArgs{
			ExternalAddress: sdk.String(mwsObjectName(base, "ext-addr")),
			Project:         sdk.StringPtr(account.Project),
			Region:          sdk.StringPtr(region),
			// NatGateway left unset: absent means the project's default internet
			// gateway, which is the only one SC knows how to ask for.
		}, opts...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to reserve the external address of vm %q", resourceName)
	}

	machine, err := mwscompute.NewVirtualMachine(pctx, toResName(hostName, env), &mwscompute.VirtualMachineArgs{
		VirtualMachine: sdk.String(mwsObjectName(base, "host-0")),
		Project:        sdk.StringPtr(account.Project),
		// The BARE zone id — `ru-central1-a`, not `zones/ru-central1-a`. The one
		// field in this file that does not take a prefixed path; see mapping.go's
		// header for the three vendor confirmations.
		Zone:   sdk.StringPtr(zone),
		VmType: sdk.String(vmType),
		Hardware: &mwscompute.VirtualMachineHardwareArgs{
			Power:                   sdk.StringPtr(powerOn),
			GracefulShutdownTimeout: sdk.StringPtr(gracefulShutdownTimeout),
		},
		Os: &mwscompute.VirtualMachineOsArgs{
			Metadata: &mwscompute.VirtualMachineOsMetadataArgs{
				// The whole cloud-init seam, in one map entry. `user-data` is the key
				// the vendor's SSH example uses and the only one measured to work.
				Attributes: sdk.StringMap{
					"user-data": sdk.ToSecret(userData).(sdk.StringOutput),
				},
			},
		},
		Storage: &mwscompute.VirtualMachineStorageArgs{
			Disks: mwscompute.VirtualMachineStorageDiskArray{
				&mwscompute.VirtualMachineStorageDiskArgs{
					Name: sdk.String(bootDiskName),
					Boot: sdk.BoolPtr(true),
					Disk: &mwscompute.VirtualMachineStorageDiskDiskArgs{
						Spec: &mwscompute.VirtualMachineStorageDiskDiskSpecArgs{
							DiskType: sdk.StringPtr(diskType),
							Iops:     sdk.IntPtr(bootDiskIops),
							Size:     sdk.StringPtr(diskSizeAttr(diskBytes)),
							Source: &mwscompute.VirtualMachineStorageDiskDiskSpecSourceArgs{
								Image: sdk.StringPtr(bootImage),
							},
						},
					},
				},
			},
		},
		Network: &mwscompute.VirtualMachineNetworkArgs{
			NetworkInterfaces: mwscompute.VirtualMachineNetworkNetworkInterfaceArray{
				&mwscompute.VirtualMachineNetworkNetworkInterfaceArgs{
					// Required by the schema, unlike most of this tree.
					Name:    sdk.String(mwsObjectName(base, "nic-primary")),
					Primary: sdk.BoolPtr(true),
					Addresses: mwscompute.VirtualMachineNetworkNetworkInterfaceAddressArray{
						&mwscompute.VirtualMachineNetworkNetworkInterfaceAddressArgs{
							Address: &mwscompute.VirtualMachineNetworkNetworkInterfaceAddressAddressArgs{
								Ref: requireString(pctx, internalAddress.Metadata.Id(), "internal address metadata.id"),
							},
							// This is what binds the reserved public address to the NIC.
							// MWS has no separate "associate" resource; the association
							// IS this nested ref, which is also why
							// CloudInitNeedsEgress is false — the ordering the
							// capability exists to force is already forced here.
							OneToOneNat: &mwscompute.VirtualMachineNetworkNetworkInterfaceAddressOneToOneNatArgs{
								External: &mwscompute.VirtualMachineNetworkNetworkInterfaceAddressOneToOneNatExternalArgs{
									Address: &mwscompute.VirtualMachineNetworkNetworkInterfaceAddressOneToOneNatExternalAddressArgs{
										Ref: requireString(pctx, externalAddress.Metadata.Id(), "external address metadata.id"),
									},
								},
							},
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create vm %q", resourceName)
	}

	// One rule per declared port. Not one rule with every port in `proto_ports`,
	// even though the field is a list: a rule named after its port can be added and
	// removed one at a time as `ingressPorts` changes, where a single rule would be
	// updated in place and a mistake in it would close every port at once.
	//
	// The destination is the INTERNAL address, per the vendor's own comment: "the
	// firewall evaluates packets after the one-to-one NAT translation, so the
	// destination is the internal address of the virtual machine, not the reserved
	// external one". Using the external address here produces a rule that is
	// accepted, looks right in the console, and matches nothing.
	internalCIDR := requireString(pctx, internalAddress.Status.IpAddress(), "internal address status.ipAddress").
		ApplyT(func(ip string) string { return ip + "/32" }).(sdk.StringOutput)

	rules := make([]sdk.Resource, 0, len(ports))
	for _, port := range ports {
		// Named after the port rather than the loop index: an index renames every
		// rule after the one that was removed, and a renamed firewall rule is a
		// delete-then-create — a window in which the port is closed.
		rule, err := mwsvpc.NewFirewallRule(pctx, toResName(fmt.Sprintf("%s-fw-%s", resourceName, portSlug(port)), env),
			&mwsvpc.FirewallRuleArgs{
				FirewallRule: sdk.String(mwsObjectName(base, "fw-"+portSlug(port))),
				Project:      sdk.StringPtr(account.Project),
				Network:      network.Network,
				Direction:    sdk.String(firewallDirectionIngress),
				Action:       sdk.String(firewallActionAllow),
				Priority:     sdk.IntPtr(firewallPriority),
				Active:       sdk.BoolPtr(true),
				ProtoPorts:   sdk.StringArray{sdk.String(protoPort(port))},
				Source: &mwsvpc.FirewallRuleSourceArgs{
					Spec: &mwsvpc.FirewallRuleSourceSpecArgs{
						Cidrs: sdk.StringArray{sdk.String(sourceCIDRFor(port))},
					},
				},
				Destination: &mwsvpc.FirewallRuleDestinationArgs{
					Spec: &mwsvpc.FirewallRuleDestinationSpecArgs{
						Cidrs: sdk.StringArray{internalCIDR},
					},
				},
			}, opts...)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to open port %s on vm %q", protoPort(port), resourceName)
		}
		rules = append(rules, rule)
	}

	host := scPlugin.NewVmHost(pctx.Context(), hostName).
		User(cfg.EffectiveSshUser()).
		Port(vm.DefaultSshPort).
		Role(string(vm.RoleCompose)).
		// AddressOutput, not AddressOf: the public IP lives at
		// `status.ipAddress` on the external address, and OutputRefTo (which
		// AddressOf uses) addresses only TOP-LEVEL resource properties — it cannot
		// reach into a nested provider output.
		AddressOutput(requireString(pctx, externalAddress.Status.IpAddress(), "external address status.ipAddress")).
		PrivateKeyOf(deployKey, "privateKeyOpenssh").
		// Every rule is a dependency, not just the machine: the machine can be
		// RUNNING and still unreachable, because ingress is default-deny on this
		// cloud. Without this edge core's readiness wait would start dialling a
		// port that nothing has opened yet and attribute the timeout to a slow boot.
		DependsOn(append([]sdk.Resource{machine}, rules...)...)

	// Pinned to the key SC generated, with NO alternative path — not
	// TrustOnFirstUse, and not `cfg.HostKey` either. SC seeded this key into the
	// machine through user-data, so its public half is known before the machine
	// exists and there is nothing to trust on first use.
	//
	// Two branches that look reasonable and must not be added:
	//
	//   - TrustOnFirstUse would silently weaken every deploy on this cloud to
	//     Cloud.ru Evolution's level, and that driver only accepts it because its
	//     API refuses user-data outright.
	//   - `cfg.HostKey` as an operator override is UNREACHABLE by construction, not
	//     merely unused: `vm.validateCloud` (api pkg/clouds/vm/vm.go:541) refuses
	//     `hostKey:` for any driver whose `Capabilities.CloudInit` is true, which
	//     this driver's is, with "SC generates the machine's host key itself and
	//     pins it — a pasted key would never be used". An earlier draft of this file
	//     branched on it; the branch was dead and its comment claimed a behaviour
	//     core forbids.
	host = host.HostKeyOf(hostKey, "publicKeyOpenssh")

	return scPlugin.BuildVmHosts(host)
}

// requireString turns an optional provider output into a required one, naming the
// field when it is absent.
//
// MWS's bridged schema marks almost every nested field optional, including ones
// that are always populated on a created resource (`metadata.id`,
// `status.ipAddress`). Passing the StringPtrOutput's Elem() through would turn an
// absent value into "" — which, fed to a `ref`, is a cloud-side error naming
// nothing, and fed to AddressOutput, is a host SC tries to SSH to at the empty
// address. Failing here names the field and the resource.
//
// Preview MUST be excluded explicitly, and an earlier version of this comment was
// wrong about why. It claimed "on preview the output is unknown, the ApplyT body
// never runs, and the unknown propagates". MEASURED FALSE on the first preview this
// driver ever ran (2026-11-07): the body runs, with `v == nil`, and the deploy dies
// before creating anything with
//
//	external address status.ipAddress; SC cannot reference a resource it has
//	no id for (this is a provider or API-shape change, not a config error)
//
// — a message that accuses the provider of changing shape when nothing is wrong at
// all. The unknown-propagates rule holds for a TOP-LEVEL computed attribute; it does
// not hold here, because `status` is a nested block the bridge resolves to a known
// zero value during preview, so `status.ipAddress` is a known nil rather than an
// unknown. Any `requireString` over a nested field has this shape.
//
// Only `status.ipAddress` was observed failing, but the gate is applied at EVERY
// call site, `metadata.id` included. Which of the five the bridge hands back as a
// known nil versus an unknown is not something this code should encode: ApplyT
// bodies run in no guaranteed order, so the one error that surfaced says nothing
// about the other four, and each extra round-trip to find out costs a preview.
//
// Gating on DryRun rather than softening the nil check keeps the check that matters:
// on a real apply the resource exists, and a nil there still fails loudly. Whether
// MWS populates these two fields on a created resource is exactly what this driver's
// first apply measures, so the apply-path assertion must stay sharp.
func requireString(ctx *sdk.Context, out sdk.StringPtrOutput, field string) sdk.StringOutput {
	return out.ApplyT(func(v *string) (string, error) {
		if v == nil || *v == "" {
			if ctx.DryRun() {
				// Nothing downstream dials or sends this on a preview: the host's
				// address feeds a command provider that does not connect, and the
				// CIDR feeds a firewall rule that is not created.
				return "", nil
			}
			return "", errors.Errorf(
				"MWS returned no %s; SC cannot reference a resource it has no id for (this is a provider "+
					"or API-shape change, not a config error)", field,
			)
		}
		return *v, nil
	}).(sdk.StringOutput)
}

// portSlug is a stable, filename-safe identifier for one port range, used in both
// the Pulumi resource name and the cloud-side rule name.
//
// Derived from the PORT, never from the loop index or the operator's `label`:
// an index shifts when a port is removed and a label is free text an operator can
// edit, and either one changes the name of a rule whose underlying port did not
// move — which Pulumi performs as delete-then-create, briefly closing the port.
func portSlug(p vm.PortRange) string {
	protocol := strings.ToLower(strings.TrimSpace(p.Protocol))
	if protocol == "" {
		protocol = vm.ProtocolTcp
	}
	if p.To > p.From {
		return fmt.Sprintf("%s-%d-%d", protocol, p.From, p.To)
	}
	return fmt.Sprintf("%s-%d", protocol, p.From)
}

// sourceCIDRFor is the SOURCE of one firewall rule, and the one place
// `PortRange.Internal` is honoured.
//
// Dropping `Internal` is the expensive mistake here, and it is silent: the proto
// comment on VmPortRange says it plainly — an internal port is the kubelet or
// etcd port of a multi-node k3s cluster, and opening it to 0.0.0.0/0 is "a
// cluster anyone can take over". An earlier draft of this file decoded the field
// in portsFromProto and then passed anyIPv4 for every rule, which is exactly that
// bug and reviews as a one-word diff.
//
// The narrow source is the SUBNET CIDR, which on this driver is the right
// granularity rather than a compromise: this driver creates the network and the
// subnet itself and puts exactly one machine's NIC in it (see the function doc),
// so the subnet range IS "the other machines of this vm resource". MWS's firewall
// takes a CIDR and has no source-security-group concept, so there is nothing
// tighter to say — the same shape GCP and YC use.
//
// If the pinned-network path (`cfg.Network`) is ever implemented, this function
// must take the operator's CIDR instead of the constant — a hardcoded
// 192.168.0.0/16 against someone else's network is either over-broad or matches
// nothing.
func sourceCIDRFor(p vm.PortRange) string {
	if p.Internal {
		return defaultSubnetCIDR
	}
	return anyIPv4
}

// mwsObjectName renders a cloud-side object name from a base and a suffix.
//
// Lowercased and reduced to `[a-z0-9-]` because every name in every vendor example
// is a DNS label and the provider schema declares no validator to check against —
// so the conservative shape is the one that cannot be refused for a reason nobody
// can see. The BASE is what gets truncated when the result would exceed
// maxMwsNameLen, never the suffix: the suffix is what distinguishes the network
// from the subnet from the address, and truncating it would collide six resources
// onto one name.
func mwsObjectName(base, suffix string) string {
	name := sanitiseDNSLabel(base)
	if suffix == "" {
		return name
	}
	suffix = sanitiseDNSLabel(suffix)

	if budget := maxMwsNameLen - len(suffix) - 1; len(name) > budget {
		if budget < 1 {
			// A suffix alone over the limit is a bug in this file, not in the
			// operator's config — every call site passes a short literal.
			return suffix
		}
		name = strings.TrimRight(name[:budget], "-")
	}
	return name + "-" + suffix
}

func sanitiseDNSLabel(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		default:
			return '-'
		}
	}, s)
	// A leading digit or hyphen is invalid in a DNS label; a trailing hyphen is
	// what `name--env` degrades to when the env is empty.
	mapped = strings.Trim(mapped, "-")
	if mapped == "" {
		return "sc"
	}
	if mapped[0] >= '0' && mapped[0] <= '9' {
		return "sc-" + mapped
	}
	return mapped
}

// portsFromProto converts the wire port list onto this cloud's internal shape.
func portsFromProto(ports []*scpluginv1.VmPortRange) []vm.PortRange {
	out := make([]vm.PortRange, 0, len(ports))
	for _, p := range ports {
		out = append(out, vm.PortRange{
			Label:    p.GetLabel(),
			Protocol: p.GetProtocol(),
			From:     int(p.GetFrom()),
			To:       int(p.GetTo()),
			Internal: p.GetInternal(),
		})
	}
	return out
}

// toResName mirrors api.ResourceInput.ToResName: `<name>` or `<name>--<env>` so
// the plugin's registered URNs match the formula SC core expects, and the provider
// name formula on which the no-destroy invariant hangs.
func toResName(name, env string) string {
	if env == "" {
		return name
	}
	return name + "--" + env
}
