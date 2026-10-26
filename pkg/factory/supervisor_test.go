//nolint:testpackage // Exercises the private counted producer boundary.
package factory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type supervisorTestBackend struct {
	j            *managedJournal
	starts       int
	startErr     error
	acknowledge  bool
	onStart      func(context.Context, ScopeHandle) error
	exit         bool
	retireErr    error
	failRetireID string
	retired      []string
	onQuiet      func(context.Context) error
}

func (b *supervisorTestBackend) Invoke(context.Context, HostInvocation) (HostResult, error) {
	return HostResult{}, errors.New("unexpected invocation")
}

func (b *supervisorTestBackend) ReleaseHostReservation(context.Context, string) error {
	return errors.New("unexpected release")
}

func (b *supervisorTestBackend) StartController(ctx context.Context, s ScopeHandle) error {
	b.starts++
	var count int
	if err := b.j.conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_reservations WHERE kind='controller' AND token=?", s.ID).Scan(&count); err != nil || count != 1 {
		return errors.New("missing committed controller token")
	}
	if _, err := b.j.Observe(ctx, s); err != nil {
		return err
	}
	if b.startErr != nil {
		return b.startErr
	}
	if b.acknowledge {
		if err := b.j.RecordHello(ctx, s, 123, 123); err != nil {
			return err
		}
		if b.onStart != nil {
			return b.onStart(ctx, s)
		}
		return nil
	}
	return nil
}

func (b *supervisorTestBackend) Retire(ctx context.Context, s ScopeHandle) error {
	b.retired = append(b.retired, s.ID)
	if b.retireErr != nil && (b.failRetireID == "" || b.failRetireID == s.ID) {
		return b.retireErr
	}
	return b.j.MarkQuiescent(ctx, s)
}

func (b *supervisorTestBackend) Quiescent(ctx context.Context, s ScopeHandle) (bool, error) {
	if b.onQuiet != nil {
		f := b.onQuiet
		b.onQuiet = nil
		if err := f(ctx); err != nil {
			return false, err
		}
	}
	if b.exit {
		if err := b.j.MarkQuiescent(ctx, s); err != nil {
			return false, err
		}
	}
	o, e := b.j.Observe(ctx, s)
	return o.Phase == "quiescent", e
}

func TestSuperviseControllerStart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ack      bool
		startErr error
		want     string
	}{{"false acknowledgement", false, nil, "acknowledgement"}, {"uncertain start", false, errors.New("start failed"), "start uncertain"}} {
		t.Run(tc.name, func(t *testing.T) {
			root, spec, reservation := managedAdmissionFixture(t)
			if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
				t.Fatal(err)
			}
			ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
				t.Fatal(err)
			}
			j, err := openManagedJournal(t.Context(), root, spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			b := &supervisorTestBackend{j: j, acknowledge: tc.ack, startErr: tc.startErr}
			_, err = Supervise(t.Context(), root, spec.ID, b)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if b.starts != 1 {
				t.Fatalf("starts = %d", b.starts)
			}
			state, err := j.supervision(t.Context())
			if err != nil || state.DesiredState != "stopped" {
				t.Fatalf("supervision = %+v, %v", state, err)
			}
		})
	}
}

func TestSuperviseControllerOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		terminal  bool
		exit      bool
		cancel    bool
		retireErr bool
		want      string
	}{
		{name: "terminal", terminal: true},
		{name: "premature exit", exit: true, want: "before delivery"},
		{name: "cancel", cancel: true, want: "context canceled"},
		{name: "uncertain retirement", retireErr: true, want: "retire uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, spec, reservation := managedAdmissionFixture(t)
			if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
				t.Fatal(err)
			}
			ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
				t.Fatal(err)
			}
			j, err := openManagedJournal(t.Context(), root, spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b := &supervisorTestBackend{j: j, acknowledge: true, exit: tc.exit}
			if tc.retireErr {
				b.retireErr = errors.New("retire uncertain")
			}
			b.onStart = func(ctx context.Context, _ ScopeHandle) error {
				if tc.cancel {
					cancel()
					return nil
				}
				if tc.retireErr {
					return errors.New("start failed")
				}
				if tc.terminal {
					store := &factoryStore{conn: j.conn}
					state, e := store.load(ctx)
					if e != nil {
						return e
					}
					state.State = "completed"
					return store.save(ctx, state, state.Revision)
				}
				return nil
			}
			status, err := Supervise(ctx, root, spec.ID, b)
			if tc.want == "" {
				if err != nil || status.State != "completed" {
					t.Fatalf("status=%+v error=%v", status, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
			if b.starts != 1 {
				t.Fatalf("starts=%d", b.starts)
			}
			supervision, err := j.supervision(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := "stopped"
			if tc.retireErr {
				want = "stopping"
			}
			if supervision.DesiredState != want {
				t.Fatalf("state=%s want=%s", supervision.DesiredState, want)
			}
		})
	}
}

func TestSuperviseDuplicateLock(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	release, err := lockManagedSupervisor(root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	j, err := openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	b := &supervisorTestBackend{j: j}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = Supervise(ctx, root, spec.ID, b)
	if err == nil || b.starts != 0 {
		t.Fatalf("error=%v starts=%d", err, b.starts)
	}
}

func TestSuperviseOldScopeAndBackoff(t *testing.T) {
	j, gen := controllerFixture(t)
	old, err := reserveManagedController(t.Context(), j, "supervisor", gen)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.RecordHello(t.Context(), old, 123, 123); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(250 * time.Millisecond)
	if err = j.setDesired(t.Context(), gen, "running", "", next); err != nil {
		t.Fatal(err)
	}
	b := &supervisorTestBackend{j: j, acknowledge: true, exit: true}
	b.onStart = func(ctx context.Context, _ ScopeHandle) error {
		if len(b.retired) != 1 || b.retired[0] != old.ID {
			return errors.New("old scope not retired before start")
		}
		if time.Now().Before(next) {
			return errors.New("started before backoff")
		}
		return nil
	}
	_, err = Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err == nil || !strings.Contains(err.Error(), "before delivery") {
		t.Fatalf("error=%v", err)
	}
	if b.starts != 1 {
		t.Fatalf("starts=%d", b.starts)
	}
}

func TestSupervisePromotionDenied(t *testing.T) {
	j, _ := controllerFixture(t)
	store := &factoryStore{conn: j.conn}
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.State = "promoting"
	if err = store.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	b := &supervisorTestBackend{j: j}
	_, err = Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err == nil || !strings.Contains(err.Error(), "promotion reconciliation unavailable") || b.starts != 0 {
		t.Fatalf("error=%v starts=%d", err, b.starts)
	}
}

func TestSuperviseBackoffDeadline(t *testing.T) {
	j, gen := controllerFixture(t)
	next := time.Now().Add(2 * time.Second)
	if err := j.setDesired(t.Context(), gen, "running", "", next); err != nil {
		t.Fatal(err)
	}
	b := &supervisorTestBackend{j: j}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := Supervise(ctx, j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if !errors.Is(err, context.DeadlineExceeded) || b.starts != 0 {
		t.Fatalf("error=%v starts=%d", err, b.starts)
	}
	state, err := j.supervision(t.Context())
	if err != nil || state.DesiredState != "stopped" {
		t.Fatalf("state=%+v error=%v", state, err)
	}
}

func TestSuperviseDockerDenial(t *testing.T) {
	j, gen := controllerFixture(t)
	target := journalScope("a", gen)
	target.Kind = "volume"
	target.Backend = "docker"
	target.Name = "oro-factory-" + cleanupObjectiveHash(j.admission) + "-volume-" + target.ID + "-" + target.Nonce
	if _, err := j.reserveScope(t.Context(), target, j.admission.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	b := &supervisorTestBackend{j: j}
	_, err := Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err == nil || !strings.Contains(err.Error(), "docker reconciliation unavailable") || b.starts != 0 || len(b.retired) != 0 {
		t.Fatalf("error=%v starts=%d retired=%v", err, b.starts, b.retired)
	}
	state, err := j.supervision(t.Context())
	if err != nil || state.DesiredState != "stopping" {
		t.Fatalf("state=%+v error=%v", state, err)
	}
}

func TestSuperviseOriginalWorkDeadline(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	spec.TimeoutSeconds = 2
	spec.StepTimeoutSeconds = 1
	reservation = reservationForIntendedSpec(t, reservation, spec)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	j, err := openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	deadline := j.admission.Deadline
	hardStop := j.admission.HardStop
	b := &supervisorTestBackend{j: j, acknowledge: true}
	b.onStart = func(ctx context.Context, s ScopeHandle) error {
		observation, err := j.Observe(ctx, s)
		if err != nil {
			return err
		}
		if !observation.Deadline.Equal(deadline) {
			return errors.New("scope deadline changed")
		}
		return nil
	}
	status, err := Supervise(t.Context(), root, spec.ID, b)
	if !errors.Is(err, context.DeadlineExceeded) || b.starts != 1 || status.State == "completed" {
		t.Fatalf("status=%+v error=%v starts=%d", status, err, b.starts)
	}
	if time.Now().Before(deadline) || !time.Now().Before(hardStop) {
		t.Fatalf("completion outside admitted bounds: deadline=%v hardstop=%v", deadline, hardStop)
	}
	state, err := j.supervision(t.Context())
	if err != nil || state.DesiredState != "stopped" {
		t.Fatalf("state=%+v error=%v", state, err)
	}
}

func TestSuperviseRetiresMultipleOldScopesInOrder(t *testing.T) {
	j, gen := controllerFixture(t)
	var ids []string
	for _, letter := range []string{"c", "a", "b"} {
		scope := journalScope(letter, gen)
		scope.Name = "dev.getoro.factory." + cleanupObjectiveHash(j.admission) + ".host." + scope.ID + "." + scope.Nonce
		if _, err := j.reserveBudget(t.Context(), gen, "host", scope.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := j.reserveScope(t.Context(), scope, j.admission.Deadline, time.Now()); err != nil {
			t.Fatal(err)
		}
		if letter == "a" {
			if err := j.RecordHello(t.Context(), scope, 123, 123); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, scope.ID)
	}
	b := &supervisorTestBackend{j: j, acknowledge: true, exit: true}
	b.onStart = func(context.Context, ScopeHandle) error {
		if len(b.retired) != 3 || b.retired[0] != ids[1] || b.retired[1] != ids[2] || b.retired[2] != ids[0] {
			return errors.New("old scopes retired out of order")
		}
		return nil
	}
	_, err := Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err == nil || !strings.Contains(err.Error(), "before delivery") || b.starts != 1 {
		t.Fatalf("error=%v starts=%d order=%v", err, b.starts, b.retired)
	}
}

type supervisorNoEffects struct{ calls int }

func (b *supervisorNoEffects) Invoke(context.Context, HostInvocation) (HostResult, error) {
	b.calls++
	return HostResult{}, errors.New("unexpected invoke")
}

func (b *supervisorNoEffects) Retire(context.Context, ScopeHandle) error {
	b.calls++
	return errors.New("unexpected retire")
}

func (b *supervisorNoEffects) Quiescent(context.Context, ScopeHandle) (bool, error) {
	b.calls++
	return false, errors.New("unexpected quiescence")
}

func (b *supervisorNoEffects) StartController(context.Context, ScopeHandle) error {
	b.calls++
	return errors.New("unexpected start")
}

func (b *supervisorNoEffects) ReleaseHostReservation(context.Context, string) error {
	b.calls++
	return errors.New("unexpected release")
}

func TestSuperviseInvalidStateHasNoBackendEffects(t *testing.T) {
	for _, name := range []string{"nil backend", "noncanonical root", "invalid id", "missing admission", "corrupt admission", "pending activation", "missing activation", "corrupt activation"} {
		t.Run(name, func(t *testing.T) {
			root, spec, reservation := managedAdmissionFixture(t)
			if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
				t.Fatal(err)
			}
			if name != "pending activation" {
				ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
				if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
					t.Fatal(err)
				}
			}
			j, err := openManagedJournal(t.Context(), root, spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			query := map[string]string{"missing admission": "DELETE FROM managed_admissions", "corrupt admission": "UPDATE managed_admissions SET body='{}'", "missing activation": "DELETE FROM managed_activation", "corrupt activation": "UPDATE managed_activation SET body='{}'"}[name]
			if query != "" {
				if _, err = j.conn.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			rootArg, idArg := root, spec.ID
			if name == "noncanonical root" {
				rootArg = root + "/../" + filepath.Base(root)
			}
			if name == "invalid id" {
				idArg = "../bad"
			}
			b := &supervisorNoEffects{}
			var backend SupervisorBackend = b
			if name == "nil backend" {
				backend = nil
			}
			_, err = Supervise(t.Context(), rootArg, idArg, backend)
			if err == nil || b.calls != 0 {
				t.Fatalf("error=%v backend calls=%d", err, b.calls)
			}
		})
	}
}

func TestSuperviseStoppedHasNoBackendEffects(t *testing.T) {
	j, gen := controllerFixture(t)
	if err := j.setDesired(t.Context(), gen, "stopping", "test", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := j.setDesired(t.Context(), gen, "stopped", "test", time.Time{}); err != nil {
		t.Fatal(err)
	}
	b := &supervisorNoEffects{}
	status, err := Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err != nil || b.calls != 0 || status.ID != j.admission.ObjectiveID || status.State != "pending" {
		t.Fatalf("status=%+v error=%v backend calls=%d", status, err, b.calls)
	}
}

func TestSuperviseInvalidDiagnosticStillRetires(t *testing.T) {
	j, _ := controllerFixture(t)
	b := &supervisorTestBackend{j: j, acknowledge: true}
	cause := errors.New("backend\x00failure")
	b.onStart = func(context.Context, ScopeHandle) error { return cause }
	_, err := Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if !errors.Is(err, cause) || len(b.retired) != 1 {
		t.Fatalf("error=%v retired=%v", err, b.retired)
	}
	state, e := j.supervision(t.Context())
	if e != nil || state.DesiredState != "stopped" {
		t.Fatalf("state=%+v error=%v", state, e)
	}
}

func TestSuperviseDetectsSupersededGeneration(t *testing.T) {
	j, _ := controllerFixture(t)
	b := &supervisorTestBackend{j: j, acknowledge: true}
	var successor managedSupervision
	var events int
	b.onQuiet = func(ctx context.Context) error {
		if _, _, err := j.beginGeneration(ctx, "successor", time.Now()); err != nil {
			return err
		}
		store := &factoryStore{conn: j.conn}
		state, err := store.load(ctx)
		if err != nil {
			return err
		}
		state.State = "completed"
		if err = store.save(ctx, state, state.Revision); err != nil {
			return err
		}
		successor, err = j.supervision(ctx)
		if err != nil {
			return err
		}
		return j.conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_events").Scan(&events)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := Supervise(ctx, j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err == nil || !strings.Contains(err.Error(), "supervisor superseded") {
		t.Fatalf("error=%v", err)
	}
	after, e := j.supervision(t.Context())
	if e != nil || after != successor || len(b.retired) != 0 {
		t.Fatalf("successor=%+v after=%+v retired=%v error=%v", successor, after, b.retired, e)
	}
	var afterEvents int
	if e = j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&afterEvents); e != nil || afterEvents != events {
		t.Fatalf("events=%d after=%d error=%v", events, afterEvents, e)
	}
}

func TestSuperviseRetiresLaunchdDespiteDocker(t *testing.T) {
	j, gen := controllerFixture(t)
	controller, err := reserveManagedController(t.Context(), j, "supervisor", gen)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.RecordHello(t.Context(), controller, 123, 123); err != nil {
		t.Fatal(err)
	}
	target := journalScope("0", gen)
	target.Kind = "volume"
	target.Backend = "docker"
	target.Name = "oro-factory-" + cleanupObjectiveHash(j.admission) + "-volume-" + target.ID + "-" + target.Nonce
	if _, err = j.reserveScope(t.Context(), target, j.admission.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	b := &supervisorTestBackend{j: j}
	_, err = Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err == nil || !strings.Contains(err.Error(), "docker reconciliation unavailable") || b.starts != 0 || len(b.retired) != 1 || b.retired[0] != controller.ID {
		t.Fatalf("error=%v starts=%d retired=%v", err, b.starts, b.retired)
	}
	obs, err := j.Observe(t.Context(), controller)
	if err != nil || obs.Phase != "quiescent" {
		t.Fatalf("controller=%+v error=%v", obs, err)
	}
}

func TestSuperviseRetiresLaterLaunchdAfterFailure(t *testing.T) {
	j, gen := controllerFixture(t)
	var scopes []ScopeHandle
	for _, letter := range []string{"a", "b"} {
		scope := journalScope(letter, gen)
		scope.Name = "dev.getoro.factory." + cleanupObjectiveHash(j.admission) + ".host." + scope.ID + "." + scope.Nonce
		if _, err := j.reserveBudget(t.Context(), gen, "host", scope.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := j.reserveScope(t.Context(), scope, j.admission.Deadline, time.Now()); err != nil {
			t.Fatal(err)
		}
		scopes = append(scopes, scope)
	}
	b := &supervisorTestBackend{j: j, retireErr: errors.New("retire failed"), failRetireID: scopes[0].ID}
	_, err := Supervise(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID, b)
	if err == nil || b.starts != 0 {
		t.Fatalf("error=%v starts=%d", err, b.starts)
	}
	obs, e := j.Observe(t.Context(), scopes[1])
	if e != nil || obs.Phase != "quiescent" {
		t.Fatalf("later scope=%+v error=%v retired=%v", obs, e, b.retired)
	}
}

func TestSuperviseCancellationCause(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	j, err := openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	operation := errors.New("recognizable start failure")
	b := &supervisorTestBackend{j: j, acknowledge: true, onStart: func(context.Context, ScopeHandle) error { cancel(); return operation }}
	_, err = Supervise(ctx, root, spec.ID, b)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, operation) {
		t.Fatalf("missing cancellation or operation cause: %v", err)
	}
	if b.starts != 1 || len(b.retired) != 1 {
		t.Fatalf("starts=%d retired=%v", b.starts, b.retired)
	}
	scopes, err := j.scopes(t.Context())
	if err != nil || len(scopes) != 1 || scopes[0].Phase != "quiescent" {
		t.Fatalf("scopes=%+v err=%v", scopes, err)
	}
	state, err := j.supervision(t.Context())
	if err != nil || state.DesiredState != "stopped" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
