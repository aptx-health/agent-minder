package controlapi

import (
	"errors"

	"github.com/aptx-health/agent-minder/internal/coordinator"
)

var ErrProviderUnavailable = errors.New("control API state provider unavailable")

// Service maps the Coordinator-owned provider boundary into frozen v1 DTOs.
// It never accepts a database Store or reaches into execution-plane internals.
type Service struct {
	Provider     coordinator.StateProvider
	BuildVersion string
}

func (s Service) Meta() (ResourceEnvelope[Meta], error) {
	if s.Provider == nil {
		return ResourceEnvelope[Meta]{}, ErrProviderUnavailable
	}
	marker, err := s.Provider.SnapshotMarker()
	if err != nil {
		return ResourceEnvelope[Meta]{}, err
	}
	return NewResourceEnvelope(Meta{
		APIVersion: APIVersion, BuildVersion: s.BuildVersion,
		Mode: WorkerMode, Capabilities: ImplementedCapabilities(),
	}, Snapshot{
		Watermark:   marker.Watermark,
		LogEpoch:    marker.LogEpoch,
		Incarnation: s.Provider.WorkerIncarnation(),
	}), nil
}
