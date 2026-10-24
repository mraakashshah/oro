package factory //nolint:testpackage // Exercise inherited labels at the private keeper ownership boundary.

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagedKeeperInheritedLabels(t *testing.T) {
	t.Run("actual inherited build label binds with creation identity", testKeeperInheritedLabelBind)
	t.Run("full source preparation and cleanup preserve complete labels", testKeeperInheritedSource)
	t.Run("bound labels cannot change through inspection or replay", testKeeperInheritedContinuity)
	t.Run("first discovery enforces exact ownership and descriptor bounds", testKeeperInheritedBounds)
	t.Run("first binding is atomic and unbound records remain exact six", testKeeperInheritedAtomicity)
}

// Captured independently from production image call-18 and guard call-20 in
// guard-image-native-73164a1597a3e2e24c8119d060a6ddbf. Docker inherited this
// label from the immutable derived image; it was not a keeper create flag.
func addCapturedKeeperImageLabel(t *testing.T, result *HostResult) {
	t.Helper()
	var objects []map[string]any
	if err := json.Unmarshal(result.Stdout, &objects); err != nil {
		t.Fatal(err)
	}
	objects[0]["Config"].(map[string]any)["Labels"].(map[string]any)["dev.getoro.factory.guard-build"] = "11dc0060bbf9b17b35df1bd133fb6673"
	var err error
	result.Stdout, err = json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
}

func testKeeperInheritedLabelBind(t *testing.T) {
	f := newSourceKeeperFixture(t)
	f.runtime.change = func(stage string, _ HostInvocation, result *HostResult) error {
		if stage == "inspect" {
			addCapturedKeeperImageLabel(t, result)
		}
		return nil
	}
	keeper, err := f.adapter.createSourceKeeper(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(keeper.Binding.Labels) != 7 || keeper.Binding.Labels["dev.getoro.factory.guard-build"] != "11dc0060bbf9b17b35df1bd133fb6673" || keeper.Binding.CreatedAt == "" {
		t.Fatal("complete inherited label map was not bound with creation identity")
	}
	loaded, err := loadManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Binding.Labels["dev.getoro.factory.guard-build"] != keeper.Binding.Labels["dev.getoro.factory.guard-build"] {
		t.Fatal("durable binding lost inherited image label")
	}
}

func testKeeperInheritedSource(t *testing.T) {
	f := newSourcePrepareFixture(t)
	before := dockerVolumeCounts(t, f.journal)
	f.runtime.base.change = func(stage string, _ HostInvocation, result *HostResult) error {
		if stage == "inspect" {
			addCapturedKeeperImageLabel(t, result)
		}
		return nil
	}
	source, err := f.adapter.prepareSource(t.Context(), f.entries)
	if err != nil {
		t.Fatal(err)
	}
	keeper, err := loadManagedDockerKeeper(t.Context(), f.journal, source.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	if len(keeper.Binding.Labels) != 7 || keeper.Binding.Labels["dev.getoro.factory.guard-build"] != "11dc0060bbf9b17b35df1bd133fb6673" {
		t.Fatal("source lost inherited labels")
	}
	after := dockerVolumeCounts(t, f.journal)
	for i, delta := range [4]int64{8, 10, 1, 40} {
		if after[i]-before[i] != delta {
			t.Fatalf("counter %d differs: %d", i, after[i]-before[i])
		}
	}
	if len(f.runtime.calls) != 8 {
		t.Fatal("inheritance caused extra runtime call")
	}
	if err := f.journal.setDesired(t.Context(), f.generation, "stopping", "label cleanup test", time.Time{}); err != nil {
		t.Fatal(err)
	}
	scope, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, DockerCleanupInspect, keeper.Binding, f.manifest.HardStop)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := f.journal.CleanupPermit(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(permit.Binding, keeper.Binding) {
		t.Fatal("cleanup discarded inherited label identity")
	}
	result := cleanupResultForPermit(t, permit)
	encoded, err := EncodeDockerCleanupResult(result, permit)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDockerCleanupResult(encoded, permit)
	if err != nil || !reflect.DeepEqual(decoded.Binding, keeper.Binding) {
		t.Fatal("cleanup roundtrip changed full map", err)
	}
	for _, mode := range []string{"added", "changed", "removed"} {
		changed := result
		changed.Binding = cloneManagedDockerBinding(result.Binding)
		changeKeeperInheritedLabel(changed.Binding.Labels, mode)
		if _, err := EncodeDockerCleanupResult(changed, permit); err == nil {
			t.Fatal("cleanup accepted changed result labels")
		}
		changedPermit := permit
		changedPermit.Binding = cloneManagedDockerBinding(permit.Binding)
		changeKeeperInheritedLabel(changedPermit.Binding.Labels, mode)
		if _, err := DecodeDockerCleanupResult(encoded, changedPermit); err == nil {
			t.Fatal("cleanup accepted changed permit labels")
		}
	}
}

func changeKeeperInheritedLabel(labels map[string]string, mode string) {
	switch mode {
	case "added":
		labels["image.extra"] = "new"
	case "changed":
		labels["dev.getoro.factory.guard-build"] = "changed"
	case "removed":
		delete(labels, "dev.getoro.factory.guard-build")
	}
}

func keeperInheritedFixture(t *testing.T) (*sourceKeeperFixture, managedDockerKeeperIntent, managedDockerVolumeIntent) {
	t.Helper()
	f := newSourceKeeperFixture(t)
	f.runtime.change = func(stage string, _ HostInvocation, result *HostResult) error {
		if stage == "inspect" {
			addCapturedKeeperImageLabel(t, result)
		}
		return nil
	}
	keeper, err := f.adapter.createSourceKeeper(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	volume, err := loadManagedDockerVolume(t.Context(), f.journal, keeper.Volume)
	if err != nil {
		t.Fatal(err)
	}
	return f, keeper, volume
}

func testKeeperInheritedContinuity(t *testing.T) {
	f, keeper, volume := keeperInheritedFixture(t)
	before := dockerVolumeCounts(t, f.journal)
	for _, mode := range []string{"added", "changed", "removed"} {
		t.Run(mode, func(t *testing.T) {
			drift := keeper
			drift.Binding = cloneManagedDockerBinding(keeper.Binding)
			changeKeeperInheritedLabel(drift.Binding.Labels, mode)
			if _, err := parseManagedDockerKeeperInspection(sourceKeeperInspection(t, drift, volume), keeper, volume); err == nil {
				t.Fatal("stopped inspection adopted label drift")
			}
			running := sourcePrepareRunning(t, drift, volume, "2026-10-24T06:00:00Z")
			if _, err := parseManagedDockerRunningKeeperInspection(running, keeper, volume, "2026-10-24T06:00:00Z"); err == nil {
				t.Fatal("running inspection adopted label drift")
			}
			if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, keeper.Keeper, drift.Binding); err == nil {
				t.Fatal("rebind adopted label drift")
			}
		})
	}
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, keeper.Keeper, keeper.Binding); err != nil {
		t.Fatal(err)
	}
	if dockerVolumeCounts(t, f.journal) != before {
		t.Fatal("label drift or identical replay changed history")
	}
	keeper.Binding.Labels["dev.getoro.factory.guard-build"] = "caller mutation"
	loaded, err := loadManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper)
	if err != nil || loaded.Binding.Labels["dev.getoro.factory.guard-build"] != "11dc0060bbf9b17b35df1bd133fb6673" {
		t.Fatal("returned map aliases durable binding", err)
	}
}

func keeperLabelDiscovery(t *testing.T) (*sourceKeeperFixture, managedDockerKeeperIntent, managedDockerVolumeIntent) {
	t.Helper()
	f := newSourceKeeperFixture(t)
	keeper := f.prepare(t)
	if _, err := dispatchManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper, keeper.RuntimeConfigSHA256); err != nil {
		t.Fatal(err)
	}
	sourcePrepareRetire(t, f.journal, keeper.CreateHost)
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper, keeper.CreateHost, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	keeper, err := loadManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	volume, err := loadManagedDockerVolume(t.Context(), f.journal, keeper.Volume)
	if err != nil {
		t.Fatal(err)
	}
	return f, keeper, volume
}

func testKeeperInheritedBounds(t *testing.T) {
	f, keeper, volume := keeperLabelDiscovery(t)
	before := dockerVolumeCounts(t, f.journal)
	for _, mode := range []string{"ownership missing", "ownership changed", "entries", "key", "value", "nul key", "nul value", "running", "unacknowledged"} {
		t.Run(mode, func(t *testing.T) {
			observed := keeper
			observed.Binding = cloneManagedDockerBinding(keeper.Binding)
			observed.Binding.Labels["dev.getoro.factory.guard-build"] = "11dc0060bbf9b17b35df1bd133fb6673"
			switch mode {
			case "ownership missing":
				delete(observed.Binding.Labels, "dev.getoro.factory.scope")
			case "ownership changed":
				observed.Binding.Labels["dev.getoro.factory.scope"] = strings.Repeat("f", 32)
			case "entries":
				for i := 0; i < 11; i++ {
					observed.Binding.Labels[fmt.Sprint(i)] = "extra"
				}
			case "key":
				observed.Binding.Labels[strings.Repeat("k", 129)] = "x"
			case "value":
				observed.Binding.Labels["extra"] = strings.Repeat("v", 1025)
			case "nul key":
				observed.Binding.Labels["nul\x00"] = "x"
			case "nul value":
				observed.Binding.Labels["extra"] = "nul\x00"
			}
			result := sourceKeeperInspection(t, observed, volume)
			expected := keeper
			if mode == "running" {
				result = sourcePrepareRunning(t, observed, volume, "2026-10-24T06:00:00Z")
			}
			if mode == "unacknowledged" {
				expected.Phase = "dispatching"
			}
			if _, err := parseManagedDockerKeeperInspection(result, expected, volume); err == nil {
				t.Fatal("invalid first discovery accepted")
			}
		})
	}
	if _, err := parseManagedDockerRunningKeeperInspection(sourcePrepareRunning(t, keeper, volume, "2026-10-24T06:00:00Z"), keeper, volume, ""); err == nil {
		t.Fatal("running inspection discovered unbound lifetime")
	}
	if dockerVolumeCounts(t, f.journal) != before {
		t.Fatal("bad discovery changed authority")
	}
	// Exactly sixteen entries with the maximum key/value lengths remains valid
	// if the complete canonical record fits the independently enforced byte cap.
	observed := keeper
	observed.Binding = cloneManagedDockerBinding(keeper.Binding)
	for i := 0; i < 10; i++ {
		observed.Binding.Labels[fmt.Sprintf("%0128d", i)] = strings.Repeat("v", 1024)
	}
	binding, err := parseManagedDockerKeeperInspection(sourceKeeperInspection(t, observed, volume), keeper, volume)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, keeper.Keeper, binding); err != nil {
		t.Fatal(err)
	}
}

func testKeeperInheritedAtomicity(t *testing.T) {
	f, keeper, volume := keeperLabelDiscovery(t)
	before := dockerVolumeCounts(t, f.journal)
	extra := keeper
	extra.Binding = cloneManagedDockerBinding(keeper.Binding)
	extra.Binding.Labels["dev.getoro.factory.guard-build"] = "11dc0060bbf9b17b35df1bd133fb6673"
	if _, err := encodeManagedDockerKeeper(f.journal, extra); err == nil {
		t.Fatal("unbound intent adopted image labels")
	}
	for i := 0; i < 9; i++ {
		extra.Binding.Labels[fmt.Sprint(i)] = strings.Repeat("\x01", 1024)
	}
	binding, err := parseManagedDockerKeeperInspection(sourceKeeperInspection(t, extra, volume), keeper, volume)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, keeper.Keeper, binding); err == nil || !strings.Contains(err.Error(), "canonical byte bound") {
		t.Fatalf("escaped record bound was not enforced: %v", err)
	}
	loaded, err := loadManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper)
	if err != nil || loaded.Binding.CreatedAt != "" || len(loaded.Binding.Labels) != 6 {
		t.Fatal("failed label binding partially committed", err)
	}
	if dockerVolumeCounts(t, f.journal) != before {
		t.Fatal("failed full-map binding emitted event")
	}
	// A missing ownership label remains invalid even in an otherwise bound record.
	extra.Binding.Labels = maps.Clone(keeper.Binding.Labels)
	extra.Binding.CreatedAt = "2026-10-24T05:49:04Z"
	delete(extra.Binding.Labels, "dev.getoro.factory.policy")
	if _, err := encodeManagedDockerKeeper(f.journal, extra); err == nil {
		t.Fatal("bound record lost required ownership")
	}
}
