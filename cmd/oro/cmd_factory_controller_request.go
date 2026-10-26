package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"oro/pkg/factory"
)

const factoryControllerRequestLimit = 16 << 10

type factoryControllerRequest struct {
	Version     int                             `json:"version"`
	Scope       factory.ScopeHandle             `json:"scope"`
	StateRoot   string                          `json:"state_root"`
	ObjectiveID string                          `json:"objective_id"`
	Runtime     factory.ManagedRuntimeReference `json:"runtime"`
	Deadline    time.Time                       `json:"deadline"`
}

func encodeFactoryControllerRequest(request factoryControllerRequest) ([]byte, error) {
	if err := validateFactoryControllerRequest(request); err != nil {
		return nil, err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode controller request: %w", err)
	}
	if len(data) > factoryControllerRequestLimit {
		return nil, errors.New("controller request exceeds limit")
	}
	return data, nil
}

func decodeFactoryControllerRequest(data []byte) (factoryControllerRequest, error) {
	var request factoryControllerRequest
	if len(data) > factoryControllerRequestLimit {
		return request, errors.New("controller request exceeds limit")
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return request, fmt.Errorf("decode controller request: %w", err)
	}
	canonical, err := encodeFactoryControllerRequest(request)
	if err != nil {
		return request, err
	}
	if !bytes.Equal(data, canonical) {
		return request, errors.New("controller request is not canonical")
	}
	return request, nil
}

func validateFactoryControllerRequest(request factoryControllerRequest) error {
	scope := request.Scope
	if request.Version != 1 || !factoryJobAbsolutePath(request.StateRoot) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(request.ObjectiveID) ||
		scope.Kind != "controller" || scope.Backend != "launchd" || scope.Generation == 0 {
		return errors.New("invalid controller request identity")
	}
	if !factoryJobLowerHex(scope.ID, 32) || !factoryJobLowerHex(scope.Nonce, 32) {
		return errors.New("invalid controller scope nonce")
	}
	hash := factoryLaunchdObjectiveHash(factoryLaunchdConfig{StateRoot: request.StateRoot, ObjectiveID: request.ObjectiveID})
	if scope.Name != "dev.getoro.factory."+hash+".controller."+scope.ID+"."+scope.Nonce ||
		request.Runtime.Path != filepath.Join(request.StateRoot, request.ObjectiveID, "runtime", "runtime.json") || !factoryJobLowerHex(request.Runtime.SHA256, 64) {
		return errors.New("invalid controller request binding")
	}
	if !validFactoryControllerDeadline(request.Deadline) {
		return errors.New("invalid controller deadline year")
	}
	return nil
}

func validFactoryControllerDeadline(deadline time.Time) bool {
	return !deadline.IsZero() && deadline.Location() == time.UTC && deadline.Year() >= 1 && deadline.Year() <= 9999
}
