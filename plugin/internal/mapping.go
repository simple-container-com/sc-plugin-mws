// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/simple-container-com/api/pkg/clouds/vm"
)

// This file is the whole translation layer between SC's cloud-independent `type:
// vm` config and MWS's own vocabulary. It is deliberately pure — no Pulumi, no
// SDK, no I/O — because every value it produces is a string MWS either accepts
// or 4xxs on, and the only cheap place to be wrong about those is a unit test.
//
// Three conventions from ../../CLOUD-FACTS.md are load-bearing here and are the
// reason this is a table rather than string surgery:
//
//  1. **A Terraform/Pulumi attribute takes the PREFIXED SHORT path**
//     (`vmTypes/gen-2-8`, `diskTypes/nbs-pl2`) while `metadata.id` and a
//     cross-resource reference take the FULL path
//     (`compute/projects/mws-ubuntu/images/<name>`). Measured off the generated
//     SDK: an `ImageRef` serialises short (`absolutePath()` → `projects/<p>/
//     images/<i>`), an `ImageID` serialises full (`ID()` → `ServiceSlug() + "/"
//     + …`). So the prefix depends on which of the two the field is typed as,
//     and there is no single rule to apply blindly.
//  2. **`zone` is the BARE id** — `ru-central1-a`, NOT `zones/ru-central1-a`.
//     This is the one place the "prefixed short path" pattern does not hold, the
//     generated docs give `zone` no description at all, and the wrong guess is
//     the natural one. Three independent vendor confirmations: the acctest
//     fixture (`virtual_machine_attach_disk.tf:50`), the provider's own
//     converter (`compute/converter/VirtualMachine.gen.go:174` assigns
//     `plan.Zone.ValueString()` through with no prefixing), and all four go-sdk
//     examples.
//  3. **Sizes are `<number> [unit]` strings**, unit set
//     `B KB MB GB TB PB EB ZB YB RB QB`, default `B` — and the units are BINARY
//     (`GB` == 2^30), see minDiskBytes below. The parser lowercases its input
//     and the space is optional (`^([0-9]+(?:\.[0-9]+)?)\s*([kmgtpezyrq]?b)?$`,
//     `go-sdk/pkg/apimodels/units/internal`), so `30GB`, `30 GB` and
//     `32212254720B` are all the same value.

// MWS zones and the region each belongs to, measured against the live API
// 2026-11-06 (`rm/sdk.ListZones` + `ListRegions`). Four zones, two regions.
//
// This table exists because SC core REQUIRES `region:` on a non-Regionless
// driver (`vm.validateCloud`) while MWS's provider takes only a `zone` — so the
// two values arrive from different places and nothing but this map can tell
// whether they agree. A zone in the wrong region is a deploy that places the
// machine somewhere the operator did not ask for and then fails much later, on
// a subnet or external address the region does not host.
var zoneRegions = map[string]string{
	"ru-central1-a": "ru-central1",
	"ru-central1-b": "ru-central1",
	"ru-central1-c": "ru-central1",
	"ru-siberia1-a": "ru-siberia1",
}

// vmTypeFamily is the flavour family the driver builds names in. MWS has three
// (`base-*`, `gen-*`, `gpu-a100-*`); `gen-*` is the widest grid (22 of the 40
// measured types) and is what the vendor's own example uses.
const vmTypeFamily = "gen"

// genVmTypes is the measured `gen-*` grid — the full list from
// `compute/sdk.ListVmTypes` on 2026-11-06, as an allowlist rather than a
// formula.
//
// A formula would be wrong: the grid is sparse (there is no `gen-2-32`, no
// `gen-16-16`), and a non-existent `vm_type` is a cloud-side 4xx minutes into a
// deploy rather than a config error at the start of one. Checking the pair
// locally costs a map lookup and turns that into a message that can list what
// IS available.
var genVmTypes = map[string]bool{
	"gen-2-4": true, "gen-2-8": true, "gen-2-16": true,
	"gen-4-8": true, "gen-4-16": true, "gen-4-32": true,
	"gen-8-16": true, "gen-8-32": true, "gen-8-64": true,
	"gen-12-24": true, "gen-12-48": true, "gen-12-96": true,
	"gen-16-32": true, "gen-16-64": true, "gen-16-128": true,
	"gen-24-48": true, "gen-24-96": true, "gen-24-192": true,
	"gen-32-64": true, "gen-32-128": true,
	"gen-48-96": true, "gen-48-192": true,
}

// mwsDiskType is the only disk type MWS has (`compute/sdk.ListDiskTypes`
// returned exactly one on 2026-11-06), available in all four zones.
const mwsDiskType = "nbs-pl2"

// Disk bounds from the measured `nbs-pl2` limits: `minDiskSize: "1 GB"`,
// `maxDiskSize: "16 TB"`.
//
// **MWS's size units are BINARY despite their decimal-SI spelling.** Measured
// off the parser the API models use,
// `go-sdk@v0.29.0/pkg/apimodels/units/bytesize/unit.go`: `kb = 1<<10`,
// `mb = 1<<10 kb`, `gb = 1<<30`, `tb = 1<<40` — so MWS's `GB` is exactly SC's
// `Gi`, and its `16 TB` ceiling is 16 TiB, not 16 * 10^12. That is worth knowing
// before reading a quota or a bill: the names say decimal and the arithmetic is
// binary, which is a ~10% difference at the TB end.
//
// The point of checking here rather than letting the cloud refuse is to catch an
// operator who wrote `size: { disk: 500Mi }` with a local message instead of a
// 4xx several minutes into a deploy.
const (
	minDiskBytes = 1 << 30  // MWS "1 GB" == 1 GiB
	maxDiskBytes = 16 << 40 // MWS "16 TB" == 16 TiB
)

// defaultDiskBytes is the root disk SC asks for when the operator did not.
//
// `VmConfig.EffectiveSize()` deliberately leaves `Disk` EMPTY when unset — "every
// cloud has a sane per-image root-disk default that is better than one SC
// invents" — but MWS's is unmeasured (it is on CLOUD-FACTS' open list: whether
// `spec.size` may be omitted at all), and the vendor example always supplies one.
// So a default is needed here, and 10 GB (the vendor's) is too small for the only
// thing SC puts on these machines: a docker host with a compose stack, whose
// images alone routinely pass 10 GB. 30 GiB is the smallest number that is not
// obviously going to wedge a second deploy on "no space left on device".
const defaultDiskBytes = 30 << 30

// bootDiskIops is the base IOPS of `nbs-pl2` (measured limits: base 1000, max
// 10000). Passed explicitly because the docs example and the SDK example both do,
// and an omitted `iops` is one more unmeasured default.
const bootDiskIops = 1000

// imageAlias maps an SC image alias onto the MWS system project and the dated
// image inside it.
//
// Two measured facts shape the shape of this table:
//
//   - **Images are NOT in the operator's project.** `ListImages` against your own
//     project returns 0, and every plausible catalogue name
//     (`public`, `marketplace`, `standard-images`, …) answers 403. They live in
//     system projects named `mws-<os>`. So the project cannot be defaulted and is
//     carried per-row.
//   - **A family name is not the image's name prefix.** `mws-debian-12-v20260916`
//     has family `mws-debian-12-lts`. Ubuntu's agree, Debian's don't — so
//     deriving one from the other by string surgery breaks on Debian, and both
//     halves are written out here instead.
//
// The versions are the newest of the 2026-09-16 batch, which is also the only
// batch that declares `spec.regions` — older images carry no region list, so a
// regional copy may not exist in `ru-siberia1`. Pinning a DATED image rather than
// a family is the point: a family reference silently moves the boot image under a
// live stack, and MWS gives no way to see that it moved.
var imageAlias = map[string]struct {
	Project string
	Image   string
}{
	"ubuntu-2404-lts": {Project: "mws-ubuntu", Image: "mws-ubuntu-2404-lts-v20260916"},
	"ubuntu-2204-lts": {Project: "mws-ubuntu", Image: "mws-ubuntu-2204-lts-v20260916"},
	"debian-12":       {Project: "mws-debian", Image: "mws-debian-12-v20260916"},
	"debian-11":       {Project: "mws-debian", Image: "mws-debian-11-v20260916"},
	"centos-stream-9": {Project: "mws-centos", Image: "mws-centos-stream-9-v20260916"},
}

// resolveZone picks the zone the machine lands in and checks it against the
// region SC core already required.
//
// Precedence is resource over credential: `zones:` on the vm resource is the
// operator's placement decision for THIS machine, while the auth block's `zone`
// is an account-wide default that may be shared by several resources. Core has
// already refused more than one `zones` entry (`validateCloud`), so index 0 is
// the whole list.
func resolveZone(cfg *vm.VmConfig, accountZone string) (string, error) {
	zone := accountZone
	if len(cfg.Zones) > 0 && cfg.Zones[0] != "" {
		zone = cfg.Zones[0]
	}

	region, known := zoneRegions[zone]
	if !known {
		return "", errors.Errorf(
			"zone %q does not exist in MWS; one of: %s (measured against the live API 2026-11-06). "+
				"Note the value is the BARE zone id — `ru-central1-a`, not `zones/ru-central1-a`",
			zone, strings.Join(knownZones(), ", "),
		)
	}

	// cfg.Region is REQUIRED by core for this driver (Capabilities.Regionless is
	// false), so an empty value cannot reach here through a validated config —
	// but VmHosts is an RPC and the only honest assumption about its input is
	// the one this function checks.
	if cfg.Region != "" && cfg.Region != region {
		return "", errors.Errorf(
			"zone %q belongs to region %q, but `region: %s` was requested; MWS's provider takes only a "+
				"zone, so SC cannot place a machine in a region its zone is not in",
			zone, region, cfg.Region,
		)
	}

	return zone, nil
}

// regionOfZone is the region a resolved zone belongs to. Separate from
// resolveZone because the subnet and the external address both take a `region`
// of their own and must get it from the same place the zone check did.
func regionOfZone(zone string) string {
	return zoneRegions[zone]
}

func knownZones() []string {
	out := make([]string, 0, len(zoneRegions))
	for z := range zoneRegions {
		out = append(out, z)
	}
	sort.Strings(out)
	return out
}

// vmTypeRef renders the `vm_type` attribute for a resolved size.
//
// Returns the PREFIXED SHORT path (`vmTypes/gen-2-8`), which is what a
// Terraform/Pulumi attribute takes — not the bare `gen-2-8` the Go SDK's typed
// ref constructor takes, and not the full `compute/vmTypes/gen-2-8` that comes
// back in `metadata.id`.
func vmTypeRef(size vm.VmSize) (string, error) {
	memGB, err := parseMemoryGB(size.Memory)
	if err != nil {
		return "", err
	}

	name := fmt.Sprintf("%s-%d-%d", vmTypeFamily, size.Cores, memGB)
	if !genVmTypes[name] {
		return "", errors.Errorf(
			"no MWS flavour matches `size: { cores: %d, memory: %s }` (would be %q): the %s-* grid is "+
				"SPARSE, not a product of its axes. Available: %s",
			size.Cores, size.Memory, name, vmTypeFamily, strings.Join(knownGenVmTypes(), ", "),
		)
	}
	return "vmTypes/" + name, nil
}

func knownGenVmTypes() []string {
	out := make([]string, 0, len(genVmTypes))
	for t := range genVmTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// parseMemoryGB reads SC's memory string (`4Gi`, `8Gi`, `60Gi`) as whole
// gigabytes, because that is the only shape MWS's flavour name can express: the
// second number in `gen-<vcpu>-<memGB>` is an integer count of GB.
//
// Deliberately NOT a general byte-size parser. A flavour is chosen by NAME, so a
// value that does not land on an integer GB cannot be requested at all, and
// rounding it to one silently gives the operator a machine of a different size
// than they wrote. Refusing names both the value and the reason.
func parseMemoryGB(memory string) (int, error) {
	trimmed := strings.TrimSpace(memory)
	if trimmed == "" {
		return 0, errors.Errorf("`size.memory` is empty; expected whole gigabytes, e.g. `8Gi`")
	}

	digits := strings.TrimRight(trimmed, "aAbBeEgGiImMtTkK ")
	suffix := strings.TrimSpace(strings.ToLower(trimmed[len(digits):]))

	value, err := strconv.Atoi(digits)
	if err != nil || value <= 0 {
		return 0, errors.Errorf(
			"cannot read `size.memory: %s` as a size; expected whole gigabytes, e.g. `8Gi`", memory,
		)
	}

	switch suffix {
	case "", "g", "gb", "gi", "gib":
		return value, nil
	default:
		// Deliberately does NOT suggest `<value>Gi`. For `512Mi` that would read as
		// "did you mean 512Gi", which is three orders of magnitude off and is advice
		// rather than a correction — the operator wants half a gigabyte, which this
		// cloud cannot name at all. State the rule and let vmTypeRef's error list
		// the grid.
		return 0, errors.Errorf(
			"`size.memory: %s` is not expressible as an MWS flavour: a flavour is selected by NAME "+
				"(`gen-<vcpu>-<memGB>`), so memory must be given in whole gigabytes (`8Gi`, `8G`, `8GB` "+
				"or a bare `8`). SC will not convert or round it, because a rounded value is a machine "+
				"of a size nobody asked for",
			memory,
		)
	}
}

// diskSizeBytes resolves the boot disk size in BYTES.
//
// Bytes, and rendered as `<n>B` by diskSizeAttr, rather than passing the
// operator's `30Gi` through: MWS's parser does not accept the `Gi`/`Ti`
// spellings at all (its unit set is `B KB MB … QB`), so SOME rewriting is
// mandatory. Going all the way to a byte count rather than swapping `Gi`→`GB`
// is what makes the result exact and the test assertable — it holds even if the
// binary-units measurement above is ever changed upstream, because `B` is the
// one unit that cannot be reinterpreted.
func diskSizeBytes(size vm.VmSize) (int64, error) {
	trimmed := strings.TrimSpace(size.Disk)
	if trimmed == "" {
		return defaultDiskBytes, nil
	}

	bytes, err := parseByteSize(trimmed)
	if err != nil {
		return 0, err
	}
	if bytes < minDiskBytes {
		return 0, errors.Errorf(
			"`size.disk: %s` is below MWS's minimum disk size of 1 GB (`nbs-pl2` limits, measured 2026-11-06)",
			size.Disk,
		)
	}
	if bytes > maxDiskBytes {
		return 0, errors.Errorf(
			"`size.disk: %s` is above MWS's maximum disk size of 16 TB — which is 16 TiB, because MWS's "+
				"size units are binary despite their spelling (`nbs-pl2` limits, measured 2026-11-06)",
			size.Disk,
		)
	}
	return bytes, nil
}

// diskSizeAttr renders a byte count as the `size` attribute. `B` is in MWS's
// documented unit set and is the default, so this is both explicit and exact.
func diskSizeAttr(bytes int64) string {
	return strconv.FormatInt(bytes, 10) + "B"
}

// parseByteSize reads a size string as a byte count.
//
// Every unit is binary, including the `kb`/`mb`/`gb` spellings — which is not a
// liberty taken here but agreement with MWS: its own parser defines `GB` as
// 1<<30 (see minDiskBytes). So `30Gi` and `30GB` mean the same thing to SC and
// to MWS, and an operator who writes either gets the disk they asked for.
func parseByteSize(value string) (int64, error) {
	digits := strings.TrimRight(value, "aAbBeEgGiImMtTkKpP ")
	suffix := strings.TrimSpace(strings.ToLower(value[len(digits):]))

	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.Errorf("cannot read %q as a size; expected e.g. `30Gi`", value)
	}

	var unit int64
	switch suffix {
	case "", "b":
		unit = 1
	case "ki", "kib", "k", "kb":
		unit = 1 << 10
	case "mi", "mib", "m", "mb":
		unit = 1 << 20
	case "gi", "gib", "g", "gb":
		unit = 1 << 30
	case "ti", "tib", "t", "tb":
		unit = 1 << 40
	default:
		return 0, errors.Errorf("unknown size unit in %q; one of: Ki, Mi, Gi, Ti (or a bare byte count)", value)
	}
	return n * unit, nil
}

// diskTypeRef renders the `disk_type` attribute, refusing any value but the one
// type that exists.
//
// `size.diskType` is in SC's DSL because other clouds have several. MWS has
// exactly one, so the only thing this knob can do here is be wrong — and a wrong
// value accepted and silently replaced would make the config a lie. Refusing
// names the legal value, which is more than the cloud's own 4xx does.
func diskTypeRef(size vm.VmSize) (string, error) {
	requested := strings.TrimSpace(size.DiskType)
	if requested != "" && requested != mwsDiskType && requested != "diskTypes/"+mwsDiskType {
		return "", errors.Errorf(
			"`size.diskType: %s` does not exist in MWS; the cloud has exactly one disk type, %q "+
				"(measured 2026-11-06). Leave `diskType` unset",
			size.DiskType, mwsDiskType,
		)
	}
	return "diskTypes/" + mwsDiskType, nil
}

// imageRef renders the FULL image path for `storage.disks[].disk.spec.source.image`.
//
// The full path (`compute/projects/<project>/images/<image>`) and not the short
// one, because that field is typed as an ID rather than a Ref — the docs example
// feeds it `data.mws_compute_image.image.metadata.id`, which is the full form.
//
// An image string that already contains a `/` is passed through UNCHANGED. That
// escape hatch is deliberate and is the whole reason this is not a closed enum:
// the aliases below pin dated images measured on one day, MWS publishes a new
// batch monthly, and an operator who needs a newer one (or a private image in
// their own project) must not have to wait for a plugin release. `compute/` is
// prepended when absent so both `projects/…` and `compute/projects/…` work,
// since both forms appear in vendor code.
//
// Rejected alternative, worth recording so it is not re-proposed:
// `compute.LookupImage` / `LookupImageOutput` would validate the image at plan
// time, which is strictly better information. It routes an `Invoke` through the
// plugin's `MonitorProxy` — an untested path in this architecture, and `Invoke`
// is precisely the RPC whose signature broke between pulumi sdk v3.245.0 and
// v3.274.1 (see pulumi-mws's README). A hand-spelled constant whose format is
// measured carries less risk than a working-but-unexercised code path.
func imageRef(image string) (string, error) {
	if strings.Contains(image, "/") {
		if strings.HasPrefix(image, "projects/") {
			return "compute/" + image, nil
		}
		return image, nil
	}

	entry, known := imageAlias[image]
	if !known {
		return "", errors.Errorf(
			"unknown `image: %s` for MWS; SC aliases are: %s. Or give a full image path "+
				"(`compute/projects/<system-project>/images/<image>`) — MWS boot images live in system "+
				"projects named `mws-<os>`, not in yours, and `ListImages` on your own project returns 0",
			image, strings.Join(knownImageAliases(), ", "),
		)
	}
	return fmt.Sprintf("compute/projects/%s/images/%s", entry.Project, entry.Image), nil
}

func knownImageAliases() []string {
	out := make([]string, 0, len(imageAlias))
	for a := range imageAlias {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// protoPort renders one firewall `proto_ports` entry.
//
// The case of the protocol is UNMEASURED and vendor code disagrees with itself:
// the generated docs example writes `proto_ports = ["TCP:22"]`, the go-sdk
// example writes `ProtoPorts: []string{"tcp:22"}`, and the field is a plain
// `[]string` with no enum type anywhere in the provider. Lowercase wins here
// because the go-sdk example is the one that RUNS — it creates the rule and then
// SSHes through it — and because `vm.ProtocolTcp` is already lowercase, so no
// conversion is introduced that could be the thing that is wrong.
//
// A range is rendered `tcp:<from>-<to>`, which is a GUESS: the vendor only ever
// shows a single port, and no range syntax is documented. It is on
// CLOUD-FACTS' open-questions list, and it is the first thing to check if a
// firewall rule is refused.
func protoPort(p vm.PortRange) string {
	protocol := strings.ToLower(strings.TrimSpace(p.Protocol))
	if protocol == "" {
		protocol = vm.ProtocolTcp
	}
	if p.To > p.From {
		return fmt.Sprintf("%s:%d-%d", protocol, p.From, p.To)
	}
	return fmt.Sprintf("%s:%d", protocol, p.From)
}
