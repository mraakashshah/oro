package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"oro/pkg/factory"
)

func readFactoryServiceBundle(objective *os.Root, name string, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference, oro, hash string) (factoryServiceIntent, factoryServiceRegistration, error) {
	var intent factoryServiceIntent
	var reg factoryServiceRegistration
	dir, err := openFactoryRuntimeDirectory(objective, name)
	if err != nil {
		return intent, reg, err
	}
	defer dir.Close()
	if err := factoryRuntimeDirectoryEntries(dir, []string{"intent.json", "registration.json", "service.plist"}); err != nil {
		return intent, reg, err
	}
	data, err := readFactoryRuntimeFile(dir, "intent.json", 16<<10)
	if err != nil {
		return intent, reg, err
	}
	if err := json.Unmarshal(data, &intent); err != nil {
		return intent, reg, fmt.Errorf("service intent: %w", err)
	}
	canonical, err := json.Marshal(intent)
	if err != nil || !bytes.Equal(data, canonical) {
		return intent, reg, errors.New("noncanonical service intent")
	}
	if err := validateFactoryServiceIntent(intent, view, ref, oro, hash); err != nil {
		return intent, reg, err
	}
	plist, err := readFactoryRuntimeFile(dir, "service.plist", 64<<10)
	if err != nil {
		return intent, reg, err
	}
	expected, err := factoryServicePlist(intent)
	if err != nil || !bytes.Equal(plist, expected) || factoryHostDigest(plist) != intent.PlistSHA256 {
		return intent, reg, errors.New("service plist mismatch")
	}
	reg, err = readFactoryServiceRegistration(dir, data)
	if err != nil {
		return intent, reg, err
	}
	return intent, reg, nil
}

func (access *factoryServiceAccess) transition(ctx context.Context, intent factoryServiceIntent, from, to string) error {
	if (from != "prepared" || to != "dispatching") && (from != "dispatching" || to != "registered") {
		return errors.New("invalid service transition")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service transition: %w", err)
	}
	view, err := factory.ReadManagedAdmission(ctx, access.root, access.id)
	if err != nil {
		return fmt.Errorf("service transition: %w", err)
	}
	actual, reg, err := access.prepare(ctx, view, intent.Runtime)
	if err != nil {
		return err
	}
	if actual != intent || reg.Phase != from {
		return errors.New("service transition identity or phase changed")
	}
	return access.writeServiceTransition(ctx, reg, to)
}

func validateFactoryServiceIntent(intent factoryServiceIntent, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference, oro, hash string) error {
	if intent.Version != 1 || intent.Root != view.StateRoot || intent.ID != view.ObjectiveID || intent.Runtime != ref || !intent.HardStop.Equal(view.HardStop) || intent.OroPath != oro || intent.OroSHA256 != hash || !factoryJobLowerHex(intent.Nonce, 32) || intent.Label != "dev.getoro.factory."+factoryHostDigest([]byte(intent.Root + "\x00" + intent.ID))[:16]+".supervisor."+intent.Nonce {
		return errors.New("service intent identity mismatch")
	}
	return nil
}

func readFactoryServiceRegistration(dir *os.Root, data []byte) (factoryServiceRegistration, error) {
	var reg factoryServiceRegistration
	record, err := readFactoryRuntimeFile(dir, "registration.json", 16<<10)
	if err != nil {
		return reg, err
	}
	if err := json.Unmarshal(record, &reg); err != nil {
		return reg, fmt.Errorf("service registration: %w", err)
	}
	canonical, err := json.Marshal(reg)
	if err != nil || !bytes.Equal(record, canonical) || reg.Version != 1 || reg.IntentSHA256 != factoryHostDigest(data) || (reg.Phase != "prepared" && reg.Phase != "dispatching" && reg.Phase != "registered") {
		return reg, errors.New("invalid service registration")
	}
	return reg, nil
}

func (access *factoryServiceAccess) writeServiceTransition(ctx context.Context, reg factoryServiceRegistration, to string) error {
	objective, err := os.OpenRoot(filepath.Join(access.root, access.id, "service"))
	if err != nil {
		return fmt.Errorf("service transition: %w", err)
	}
	defer objective.Close()
	if err := bindFactoryRuntimeRoot(objective, filepath.Join(access.root, access.id, "service")); err != nil {
		return err
	}
	reg.Phase = to
	data, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("service transition: %w", err)
	}
	if err := writeFactoryPublicationFile(objective, ".registration.stage", data); err != nil {
		return fmt.Errorf("service transition uncertain: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service transition: %w", err)
	}
	if err := objective.Rename(".registration.stage", "registration.json"); err != nil {
		return fmt.Errorf("service transition: %w", err)
	}
	return syncFactoryBudgetDirectory(objective)
}
