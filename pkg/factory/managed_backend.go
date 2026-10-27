package factory

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"
)

type managedDeliveryBackend struct {
	ctx    context.Context
	owner  *managedControllerOwner
	git    *managedGitRepository
	docker *managedDockerVolumes
}

func newManagedDeliveryBackend(ctx context.Context, owner *managedControllerOwner, host HostRuntime, reader ManagedDockerConfigReader) (*managedDeliveryBackend, error) {
	if owner == nil || owner.journal == nil || reader == nil || host == nil || (reflect.ValueOf(host).Kind() == reflect.Ptr && reflect.ValueOf(host).IsNil()) {
		return nil, errors.New("managed backend requires owner and reader")
	}
	git, err := newManagedGitRepository(owner.manifest, owner.scope.Generation, owner.journal, host)
	if err != nil {
		return nil, err
	}
	bound := func(ctx context.Context) (ManagedDockerConfig, error) {
		config, err := reader(ctx)
		if err != nil {
			return config, err
		}
		if config.RuntimeConfigPath != owner.runtime.Path || config.RuntimeConfigSHA256 != owner.runtime.SHA256 {
			return ManagedDockerConfig{}, errors.New("managed runtime reference changed")
		}
		return config, nil
	}
	docker, err := newManagedDockerVolumes(ctx, owner.manifest, owner.scope.Generation, owner.journal, host, bound)
	if err != nil {
		return nil, err
	}
	return &managedDeliveryBackend{ctx: ctx, owner: owner, git: git, docker: docker}, nil
}

func (b *managedDeliveryBackend) checkOwner(ctx context.Context) error {
	return b.owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		s, e := validateActivationSnapshot(ctx, tx, b.owner.manifest)
		if e != nil {
			return false, e
		}
		return false, b.owner.validateNext(ctx, tx, s)
	})
}

func interrupt(stage string, err error) error {
	return &managedInterruptionError{stage: stage, cause: err}
}

func unavailable(stage string) error {
	return interrupt(stage, errors.New("managed operation unavailable"))
}

func (b *managedDeliveryBackend) verifyPolicies() error {
	m, err := loadManagedManifest(b.ctx, b.owner.manifest.Reservation.StateRoot, b.owner.manifest.Spec.ID)
	if err != nil {
		return interrupt("policies", err)
	}
	if !reflect.DeepEqual(m, b.owner.manifest) {
		return unavailable("policies")
	}
	if err = b.checkOwner(b.ctx); err != nil {
		return interrupt("policies", err)
	}
	return nil
}
func (b *managedDeliveryBackend) freezePolicies() error { return unavailable("freeze policies") }
func (b *managedDeliveryBackend) target(ctx context.Context, deadline time.Time) (string, error) {
	if !deadline.Equal(b.owner.manifest.Deadline) {
		return "", interrupt("target", errors.New("managed objective deadline changed"))
	}
	if err := b.checkOwner(ctx); err != nil {
		return "", interrupt("target", err)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	oid, err := b.git.target(ctx)
	if err != nil {
		return "", interrupt("target", err)
	}
	return oid, nil
}

func (b *managedDeliveryBackend) pristine(context.Context, string, string, time.Time) error {
	return unavailable("pristine")
}

func (b *managedDeliveryBackend) commit(context.Context, string, string, time.Time) (string, error) {
	return "", unavailable("commit")
}

func (b *managedDeliveryBackend) exists(context.Context, string, time.Time) error {
	return unavailable("exists")
}

func (b *managedDeliveryBackend) promote(context.Context, string, string, time.Time) error {
	return unavailable("promote")
}

func (b *managedDeliveryBackend) observeTarget(context.Context) (string, error) {
	return "", unavailable("observe target")
}

func (b *managedDeliveryBackend) completedArtifact(context.Context, string) error {
	return unavailable("completed artifact")
}
