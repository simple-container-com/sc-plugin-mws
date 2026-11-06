// SPDX-License-Identifier: MIT
// Copyright (c) Simple Container

package mws

import (
	"strconv"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/simple-container-com/api/pkg/clouds/vm"
)

// mapping.go is pure, and that is the point: every value it produces is a string
// MWS either accepts or 4xxs on, and a unit test is the only cheap place to be
// wrong about one. Nothing below reaches the network.
//
// The assertions are deliberately on EXACT strings rather than on "contains
// gen-2-8": the prefix is the whole difficulty of this file (a Pulumi attribute
// takes `vmTypes/gen-2-8` while `metadata.id` returns `compute/vmTypes/gen-2-8`),
// so a test that tolerated either would pass through the bug it exists to catch.

func TestResolveZonePrefersTheResourceOverTheAccount(t *testing.T) {
	RegisterTestingT(t)

	// `zones:` on the vm resource is the placement decision for THIS machine; the
	// auth block's zone is an account-wide default several resources may share.
	zone, err := resolveZone(&vm.VmConfig{Zones: []string{"ru-central1-c"}}, "ru-central1-a")
	Expect(err).ToNot(HaveOccurred())
	Expect(zone).To(Equal("ru-central1-c"))

	// Falls back when the resource says nothing.
	zone, err = resolveZone(&vm.VmConfig{}, "ru-siberia1-a")
	Expect(err).ToNot(HaveOccurred())
	Expect(zone).To(Equal("ru-siberia1-a"))

	// An empty entry is not a decision.
	zone, err = resolveZone(&vm.VmConfig{Zones: []string{""}}, "ru-central1-b")
	Expect(err).ToNot(HaveOccurred())
	Expect(zone).To(Equal("ru-central1-b"))
}

// The region/zone cross-check is the only thing in the system that can catch a
// disagreement: core REQUIRES `region:` for this driver while MWS's provider
// takes only a `zone`, so the two values arrive from different places and neither
// side validates the pair. A zone in the wrong region deploys the machine
// somewhere the operator did not ask for and then fails much later, on a subnet
// the region does not host.
func TestResolveZoneRefusesARegionZoneMismatch(t *testing.T) {
	RegisterTestingT(t)

	_, err := resolveZone(&vm.VmConfig{
		Region: "ru-siberia1",
		Zones:  []string{"ru-central1-a"},
	}, "")
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("ru-central1"))
	Expect(err.Error()).To(ContainSubstring("ru-siberia1"))

	// Agreement passes.
	zone, err := resolveZone(&vm.VmConfig{Region: "ru-central1", Zones: []string{"ru-central1-b"}}, "")
	Expect(err).ToNot(HaveOccurred())
	Expect(zone).To(Equal("ru-central1-b"))
}

// The refusal must say the value is a BARE id. `zones/ru-central1-a` is the
// natural wrong guess — everything else in MWS's attribute vocabulary is a
// prefixed path — and the generated docs give `zone` no description at all.
func TestResolveZoneRefusesAnUnknownZoneAndNamesTheShape(t *testing.T) {
	RegisterTestingT(t)

	_, err := resolveZone(&vm.VmConfig{Zones: []string{"zones/ru-central1-a"}}, "")
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("BARE"))
	Expect(err.Error()).To(ContainSubstring("ru-central1-a"))

	_, err = resolveZone(&vm.VmConfig{Zones: []string{"eu-west-1a"}}, "")
	Expect(err).To(HaveOccurred())
}

// Four zones, two regions, measured 2026-11-06. ru-siberia1 having exactly ONE
// zone is the fact that makes `Capabilities.MultiAZInRegion: 3` a ru-central1
// statement rather than a cloud-wide one.
func TestZoneRegionTableShape(t *testing.T) {
	RegisterTestingT(t)

	Expect(knownZones()).To(Equal([]string{
		"ru-central1-a", "ru-central1-b", "ru-central1-c", "ru-siberia1-a",
	}))
	Expect(regionOfZone("ru-central1-c")).To(Equal("ru-central1"))
	Expect(regionOfZone("ru-siberia1-a")).To(Equal("ru-siberia1"))
	Expect(regionOfZone("nope")).To(BeEmpty())

	siberian := 0
	for _, r := range zoneRegions {
		if r == "ru-siberia1" {
			siberian++
		}
	}
	Expect(siberian).To(Equal(1),
		"ru-siberia1 has one zone; a driver that reads MultiAZInRegion as cloud-wide would place a second node nowhere")
}

func TestVmTypeRefRendersThePrefixedShortPath(t *testing.T) {
	RegisterTestingT(t)

	ref, err := vmTypeRef(vm.VmSize{Cores: 2, Memory: "8Gi"})
	Expect(err).ToNot(HaveOccurred())
	Expect(ref).To(Equal("vmTypes/gen-2-8"),
		"a Pulumi attribute takes the prefixed SHORT path — not the bare name the Go SDK's typed ref takes, and not the full `compute/vmTypes/...` that comes back in metadata.id")
}

// The grid is SPARSE, not a product of its axes, which is the whole reason it is
// an allowlist. `gen-2-32` and `gen-16-16` are the two holes a formula would
// invent, and either one is a cloud-side 4xx minutes into a deploy.
func TestVmTypeRefRefusesHolesInTheSparseGrid(t *testing.T) {
	RegisterTestingT(t)

	for _, size := range []vm.VmSize{
		{Cores: 2, Memory: "32Gi"},
		{Cores: 16, Memory: "16Gi"},
	} {
		_, err := vmTypeRef(size)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("SPARSE"))
		// The message must list what IS available, which is the only thing that
		// turns the refusal into an action.
		Expect(err.Error()).To(ContainSubstring("gen-2-8"))
	}
}

// Every advertised flavour must actually render, which is what keeps the
// allowlist and the name formula from drifting apart.
func TestEveryKnownVmTypeRoundTrips(t *testing.T) {
	RegisterTestingT(t)

	for _, name := range knownGenVmTypes() {
		parts := strings.Split(strings.TrimPrefix(name, vmTypeFamily+"-"), "-")
		Expect(parts).To(HaveLen(2), "flavour %q is not `gen-<vcpu>-<memGB>`", name)

		cores, err := strconv.Atoi(parts[0])
		Expect(err).ToNot(HaveOccurred())
		mem, err := strconv.Atoi(parts[1])
		Expect(err).ToNot(HaveOccurred())

		ref, err := vmTypeRef(vm.VmSize{Cores: cores, Memory: strconv.Itoa(mem) + "Gi"})
		Expect(err).ToNot(HaveOccurred(), "flavour %q is in the allowlist but vmTypeRef refuses it", name)
		Expect(ref).To(Equal("vmTypes/" + name))
	}
}

func TestParseMemoryGBAcceptsTheWholeGigabyteSpellings(t *testing.T) {
	RegisterTestingT(t)

	for _, spelling := range []string{"8Gi", "8GiB", "8G", "8GB", "8 GB", "8gb", "8"} {
		got, err := parseMemoryGB(spelling)
		Expect(err).ToNot(HaveOccurred(), "spelling %q", spelling)
		Expect(got).To(Equal(8), "spelling %q", spelling)
	}
}

// Refused rather than rounded, and the refusal must not suggest a number. For
// `512Mi` a `<value>Gi` suggestion would read as "did you mean 512Gi" — three
// orders of magnitude off, and advice rather than a correction.
func TestParseMemoryGBRefusesWhatAFlavourNameCannotExpress(t *testing.T) {
	RegisterTestingT(t)

	_, err := parseMemoryGB("512Mi")
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("whole gigabytes"))
	Expect(err.Error()).ToNot(ContainSubstring("512Gi"),
		"the message must not suggest 512Gi to someone who asked for 512Mi")

	for _, bad := range []string{"", "   ", "8Ti", "0Gi", "-4Gi", "eight"} {
		_, err := parseMemoryGB(bad)
		Expect(err).To(HaveOccurred(), "value %q", bad)
	}
}

// The binary-units measurement, which is the correction this slice carries:
// MWS's own parser (go-sdk pkg/apimodels/units/bytesize) defines `gb = 1<<30`, so
// MWS's `GB` IS SC's `Gi`. An earlier draft of mapping.go called this
// unmeasurable and set the ceiling to a decimal 16*10^12 — ~10% low.
func TestParseByteSizeIsBinaryForEverySpelling(t *testing.T) {
	RegisterTestingT(t)

	for _, spelling := range []string{"30Gi", "30GiB", "30G", "30GB", "30gb"} {
		got, err := parseByteSize(spelling)
		Expect(err).ToNot(HaveOccurred(), "spelling %q", spelling)
		Expect(got).To(Equal(int64(30)<<30), "spelling %q must be 30 GiB; MWS's GB is 2^30", spelling)
	}

	got, err := parseByteSize("1Ti")
	Expect(err).ToNot(HaveOccurred())
	Expect(got).To(Equal(int64(1) << 40))

	// A bare count is bytes.
	got, err = parseByteSize("32212254720")
	Expect(err).ToNot(HaveOccurred())
	Expect(got).To(Equal(int64(30) << 30))

	for _, bad := range []string{"", "Gi", "0Gi", "-1Gi", "30Pi", "thirty"} {
		_, err := parseByteSize(bad)
		Expect(err).To(HaveOccurred(), "value %q", bad)
	}
}

func TestDiskSizeBytesBoundsAndDefault(t *testing.T) {
	RegisterTestingT(t)

	// Unset: EffectiveSize leaves Disk empty on purpose, but MWS's own per-image
	// default is unmeasured and the vendor example always supplies a size — so the
	// driver has to pick one, and 10 GB (the vendor's) wedges a docker host.
	got, err := diskSizeBytes(vm.VmSize{})
	Expect(err).ToNot(HaveOccurred())
	Expect(got).To(Equal(int64(defaultDiskBytes)))
	Expect(got).To(Equal(int64(30) << 30))

	// The exact bounds are accepted, not just the interior.
	got, err = diskSizeBytes(vm.VmSize{Disk: "1Gi"})
	Expect(err).ToNot(HaveOccurred())
	Expect(got).To(Equal(int64(minDiskBytes)))

	got, err = diskSizeBytes(vm.VmSize{Disk: "16Ti"})
	Expect(err).ToNot(HaveOccurred())
	Expect(got).To(Equal(int64(maxDiskBytes)))

	_, err = diskSizeBytes(vm.VmSize{Disk: "500Mi"})
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("minimum"))

	// 17 TiB is over; and the message must say the ceiling is binary, because "16
	// TB" read as decimal makes a 16 TiB request look legal.
	_, err = diskSizeBytes(vm.VmSize{Disk: "17Ti"})
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("16 TiB"))
}

// Bytes on the wire, not the operator's `30Gi`: MWS's parser does not accept the
// `Gi`/`Ti` spellings at all, so some rewriting is mandatory, and `B` is the one
// unit that cannot be reinterpreted if the binary-units reading ever changes
// upstream.
func TestDiskSizeAttrIsAnExactByteCount(t *testing.T) {
	RegisterTestingT(t)

	Expect(diskSizeAttr(30 << 30)).To(Equal("32212254720B"))
	Expect(diskSizeAttr(minDiskBytes)).To(Equal("1073741824B"))
}

// MWS has exactly one disk type, so the only thing SC's `diskType` knob can do
// here is be wrong. Accepted-and-silently-replaced would make the config a lie.
func TestDiskTypeRefRefusesAnythingButTheOneTypeThatExists(t *testing.T) {
	RegisterTestingT(t)

	for _, requested := range []string{"", "nbs-pl2", "diskTypes/nbs-pl2"} {
		ref, err := diskTypeRef(vm.VmSize{DiskType: requested})
		Expect(err).ToNot(HaveOccurred(), "requested %q", requested)
		Expect(ref).To(Equal("diskTypes/nbs-pl2"))
	}

	_, err := diskTypeRef(vm.VmSize{DiskType: "network-ssd"})
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("nbs-pl2"),
		"the refusal must name the legal value, which is more than the cloud's own 4xx does")
}

// The FULL path, because that field is typed as an ID rather than a Ref (the docs
// example feeds it `metadata.id`). Every alias is asserted literally: these are
// dated images measured on one day, and a typo in one is a boot of the wrong OS
// or no boot at all.
func TestImageRefRendersFullPathsForEveryAlias(t *testing.T) {
	RegisterTestingT(t)

	expected := map[string]string{
		"ubuntu-2404-lts": "compute/projects/mws-ubuntu/images/mws-ubuntu-2404-lts-v20260916",
		"ubuntu-2204-lts": "compute/projects/mws-ubuntu/images/mws-ubuntu-2204-lts-v20260916",
		"debian-12":       "compute/projects/mws-debian/images/mws-debian-12-v20260916",
		"debian-11":       "compute/projects/mws-debian/images/mws-debian-11-v20260916",
		"centos-stream-9": "compute/projects/mws-centos/images/mws-centos-stream-9-v20260916",
	}
	Expect(knownImageAliases()).To(HaveLen(len(expected)))

	for alias, want := range expected {
		got, err := imageRef(alias)
		Expect(err).ToNot(HaveOccurred(), "alias %q", alias)
		Expect(got).To(Equal(want), "alias %q", alias)
	}

	// SC's own default must resolve, or every config that omits `image:` fails.
	_, err := imageRef(vm.DefaultImage)
	Expect(err).ToNot(HaveOccurred(),
		"SC's DefaultImage (%q) has no MWS alias, so every config that omits `image:` would be refused", vm.DefaultImage)
}

// The `/` pass-through is the escape hatch that keeps the alias table from being
// a release gate: the aliases pin dated images, MWS publishes a new batch
// monthly, and an operator who needs a newer one — or a private image in their
// own project — must not have to wait for a plugin release.
func TestImageRefPassesAFullPathThrough(t *testing.T) {
	RegisterTestingT(t)

	got, err := imageRef("compute/projects/my-proj/images/my-image")
	Expect(err).ToNot(HaveOccurred())
	Expect(got).To(Equal("compute/projects/my-proj/images/my-image"))

	// `compute/` is prepended when absent, because both forms appear in vendor code.
	got, err = imageRef("projects/my-proj/images/my-image")
	Expect(err).ToNot(HaveOccurred())
	Expect(got).To(Equal("compute/projects/my-proj/images/my-image"))

	// An unknown bare alias is refused, and the message has to explain that images
	// are NOT in the operator's project — `ListImages` on your own returns 0, so
	// "my image exists, why is this failing" is the expected next question.
	_, err = imageRef("ubuntu-2604")
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("system projects"))
	Expect(err.Error()).To(ContainSubstring("ubuntu-2404-lts"))
}

// Lowercase, because the go-sdk example is the one that RUNS (it creates the rule
// and then SSHes through it) while the generated docs example writes `TCP:22`.
// The case is on CLOUD-FACTS' open list and is the first thing to check if a rule
// is refused.
func TestProtoPortIsLowercaseAndRendersRanges(t *testing.T) {
	RegisterTestingT(t)

	Expect(protoPort(vm.PortRange{Protocol: "tcp", From: 22, To: 22})).To(Equal("tcp:22"))
	Expect(protoPort(vm.PortRange{Protocol: "TCP", From: 22, To: 22})).To(Equal("tcp:22"))
	Expect(protoPort(vm.PortRange{Protocol: " udp ", From: 51820, To: 51820})).To(Equal("udp:51820"))

	// From == To collapses to a single port, which is what every vendor example
	// shows. The range form is a GUESS — no range syntax is documented anywhere.
	Expect(protoPort(vm.PortRange{Protocol: "tcp", From: 30000, To: 32767})).To(Equal("tcp:30000-32767"))

	// An unset protocol defaults to tcp rather than producing `:22`, which MWS
	// would read as a protocol named "".
	Expect(protoPort(vm.PortRange{From: 22, To: 22})).To(Equal("tcp:22"))
}

// portSlug names the Pulumi resource AND the cloud-side rule, and must derive
// from the PORT: an index shifts when a port is removed and a label is free text,
// and either renames a rule whose port did not move — which Pulumi performs as
// delete-then-create, briefly closing the port.
func TestPortSlugIgnoresTheOperatorsLabel(t *testing.T) {
	RegisterTestingT(t)

	Expect(portSlug(vm.PortRange{Label: "ssh", Protocol: "tcp", From: 22, To: 22})).To(Equal("tcp-22"))
	Expect(portSlug(vm.PortRange{Label: "renamed-by-operator", Protocol: "tcp", From: 22, To: 22})).
		To(Equal("tcp-22"), "a label edit must not rename the rule")
	Expect(portSlug(vm.PortRange{Protocol: "TCP", From: 30000, To: 32767})).To(Equal("tcp-30000-32767"))
	Expect(portSlug(vm.PortRange{From: 80, To: 80})).To(Equal("tcp-80"))
}

// `internal: true` narrows the source to the subnet. Dropping it is the bug this
// driver shipped in draft: the kubelet and etcd ports of a multi-node cluster
// open to 0.0.0.0/0.
func TestSourceCIDRForHonoursInternal(t *testing.T) {
	RegisterTestingT(t)

	Expect(sourceCIDRFor(vm.PortRange{From: 443, To: 443})).To(Equal("0.0.0.0/0"))
	Expect(sourceCIDRFor(vm.PortRange{From: 10250, To: 10250, Internal: true})).To(Equal(defaultSubnetCIDR))
}

// Cloud-side names are bounded conservatively (63, the K8s DNS-label limit —
// the REAL limit is unmeasured) and it is the BASE that gets truncated, never the
// suffix: the suffix is what distinguishes the network from the subnet from the
// two addresses, and truncating it would collide six resources onto one name.
func TestMwsObjectNameTruncatesTheBaseNotTheSuffix(t *testing.T) {
	RegisterTestingT(t)

	Expect(mwsObjectName("web", "net")).To(Equal("web-net"))
	Expect(mwsObjectName("web", "")).To(Equal("web"))
	Expect(mwsObjectName("Web_Stack--Staging", "ext-addr")).To(Equal("web-stack--staging-ext-addr"))

	long := strings.Repeat("a", 120)
	for _, suffix := range []string{"net", "subnet", "addr", "ext-addr", "fw-tcp-30000-32767"} {
		got := mwsObjectName(long, suffix)
		Expect(len(got)).To(BeNumerically("<=", maxMwsNameLen), "suffix %q", suffix)
		Expect(got).To(HaveSuffix("-"+suffix), "suffix %q must survive truncation intact", suffix)
	}

	// Distinct suffixes must stay distinct after truncation, which is the property
	// that makes the six resources of one vm non-colliding.
	seen := map[string]bool{}
	for _, suffix := range []string{"net", "subnet", "addr", "ext-addr"} {
		name := mwsObjectName(long, suffix)
		Expect(seen[name]).To(BeFalse(), "truncation collided on %q", name)
		seen[name] = true
	}
}

func TestSanitiseDNSLabel(t *testing.T) {
	RegisterTestingT(t)

	Expect(sanitiseDNSLabel("Web-Stack")).To(Equal("web-stack"))
	Expect(sanitiseDNSLabel("web_stack.1")).To(Equal("web-stack-1"))
	Expect(sanitiseDNSLabel("--web--")).To(Equal("web"),
		"a leading or trailing hyphen is not a legal DNS label")
}
