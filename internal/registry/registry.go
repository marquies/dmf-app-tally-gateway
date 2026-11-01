// Package registry persists which source each tally device shows.
package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const fileName = "devices.json"

var ErrNotFound = errors.New("device not found")

type Device struct {
	DeviceID string    `json:"deviceId"`
	SourceID string    `json:"sourceId"`
	Firmware string    `json:"firmware,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
}

// Registry is a device map persisted as one JSON file, rewritten atomically
// on every change so a crash leaves either the old or the new file.
type Registry struct {
	mu      sync.Mutex
	path    string
	devices map[string]Device
}

// Open loads the registry from dir, starting empty when no file exists.
func Open(dir string) (*Registry, error) {
	r := &Registry{path: filepath.Join(dir, fileName), devices: map[string]Device{}}
	b, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Device
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	for _, d := range list {
		r.devices[d.DeviceID] = d
	}
	return r, nil
}

// Register records a device connecting and returns its record. A known
// device keeps its persisted source, whatever it registered with: the
// assignment an operator made wins over the device's own configuration.
func (r *Registry) Register(deviceID, sourceID, firmware string, now time.Time) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok {
		d = Device{DeviceID: deviceID, SourceID: sourceID}
	}
	d.Firmware = firmware
	d.LastSeen = now
	r.devices[deviceID] = d
	return d, r.save()
}

// Assign points a known device at a source.
func (r *Registry) Assign(deviceID, sourceID string) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok {
		return Device{}, ErrNotFound
	}
	d.SourceID = sourceID
	r.devices[deviceID] = d
	return d, r.save()
}

// Seen records when a device was last heard from. Unknown devices are
// ignored: a device deleted while connected stays deleted.
func (r *Registry) Seen(deviceID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok {
		return nil
	}
	d.LastSeen = now
	r.devices[deviceID] = d
	return r.save()
}

func (r *Registry) Delete(deviceID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.devices[deviceID]; !ok {
		return ErrNotFound
	}
	delete(r.devices, deviceID)
	return r.save()
}

func (r *Registry) Get(deviceID string) (Device, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	return d, ok
}

// List returns every device sorted by id.
func (r *Registry) List() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list()
}

func (r *Registry) list() []Device {
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}

func (r *Registry) save() error {
	b, err := json.MarshalIndent(r.list(), "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), fileName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), r.path)
}
