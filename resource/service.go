// +build linux

package resource

import (
	"errors"
	"fmt"
	"log"

	"github.com/coreos/go-systemd/dbus"
	"github.com/coreos/go-systemd/util"
	"github.com/hashicorp/hcl"
	"github.com/hashicorp/hcl/hcl/ast"
	"github.com/imdario/mergo"
)

// Name of the service resource type
const serviceResourceType = "service"

// ServiceResource type is a resource which manages
// services on a GNU/Linux system running systemd
type ServiceResource struct {
	BaseResource `hcl:",squash"`

	// If true then enable service during boot-time
	Enable bool `hcl:"enable" json:"enable"`

	// Systemd unit name
	UnitName string `hcl:"-" json:"-"`
}

// NewServiceResource creates a new resource for managing services
// using systemd on a GNU/Linux system
func NewServiceResource(obj *ast.ObjectItem) (Resource, error) {
	// Resource defaults
	defaults := &ServiceResource{
		BaseResource{
			ResourceType: serviceResourceType,
			State:        StateRunning,
		},
		false, // Enables the service by default
		"",    // Service unit name is set below
	}

	var s ServiceResource
	err := hcl.DecodeObject(&s, obj)
	if err != nil {
		return nil, err
	}

	// Merge the decoded object with the resource defaults
	err = mergo.Merge(&s, defaults)

	// Set unit name
	s.UnitName = fmt.Sprintf("%s.service", s.Name)

	return &s, nil
}

// unitProperty retrieves the requested property for the service unit
func (s *ServiceResource) unitProperty(propertyName string) (*dbus.Property, error) {
	conn, err := dbus.New()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	property, err := conn.GetUnitProperty(s.UnitName, propertyName)

	return property, err
}

// unitIsEnabled checks if the unit is enabled or disabled
func (s *ServiceResource) unitIsEnabled() (bool, error) {
	unitState, err := s.unitProperty("UnitFileState")
	if err != nil {
		return false, err
	}

	value := unitState.Value.Value().(string)
	switch value {
	case "enabled", "static", "enabled-runtime", "linked", "linked-runtime":
		return true, nil
	case "disabled", "masked", "masked-runtime":
		return false, nil
	case "invalid":
		fallthrough
	default:
		return false, errors.New("Invalid unit state")
	}
}

// enableUnit enables the service unit during boot-time
func (s *ServiceResource) enableUnit() error {
	conn, err := dbus.New()
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("%s enabling service", s.ID())

	units := []string{s.UnitName}
	_, changes, err := conn.EnableUnitFiles(units, false, false)
	if err != nil {
		return err
	}

	for _, change := range changes {
		log.Printf("%s %s %s -> %s", s.ID(), change.Type, change.Filename, change.Destination)
	}

	return nil
}

// disableUnit disables the service unit during boot-time
func (s *ServiceResource) disableUnit() error {
	conn, err := dbus.New()
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("%s disabling service", s.ID())

	units := []string{s.UnitName}
	changes, err := conn.DisableUnitFiles(units, false)
	if err != nil {
		return err
	}

	for _, change := range changes {
		log.Printf("%s %s %s", s.ID(), change.Type, change.Filename)
	}

	return nil
}

// daemonReload instructs systemd to scan for and reload unit files
func (s *ServiceResource) daemonReload() error {
	conn, err := dbus.New()
	if err != nil {
		return err
	}
	defer conn.Close()

	return conn.Reload()
}

// Evaluate evaluates the state of the resource
func (s *ServiceResource) Evaluate() (State, error) {
	resourceState := State{
		Current: StateUnknown,
		Want:    s.State,
		Update:  false,
	}

	// Check if the unit is started/stopped
	activeState, err := s.unitProperty("ActiveState")
	if err != nil {
		return resourceState, err
	}

	// TODO: Handle cases where the unit is not found

	value := activeState.Value.Value().(string)
	switch value {
	case "active", "reloading", "activating":
		resourceState.Current = StateRunning
	case "inactive", "failed", "deactivating":
		resourceState.Current = StateStopped
	}

	// Check if the unit is enabled/disabled
	enabled, err := s.unitIsEnabled()
	if err != nil {
		return resourceState, err
	}

	// Check if the resource needs to be updated
	if s.Enable != enabled {
		resourceState.Update = true
	}

	return resourceState, nil
}

// Create starts the service unit
func (s *ServiceResource) Create() error {
	conn, err := dbus.New()
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("%s starting service", s.ID())

	ch := make(chan string)
	jobID, err := conn.StartUnit(s.UnitName, "replace", ch)
	if err != nil {
		return err
	}

	result := <-ch
	log.Printf("%s systemd job id %d result: %s", s.ID(), jobID, result)

	return nil
}

// Delete stops the service unit
func (s *ServiceResource) Delete() error {
	conn, err := dbus.New()
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("%s stopping service", s.ID())

	ch := make(chan string)
	jobID, err := conn.StopUnit(s.UnitName, "replace", ch)
	if err != nil {
		return err
	}

	result := <-ch
	log.Printf("%s systemd job id %d result: %s", s.ID(), jobID, result)

	return nil
}

// Update updates the service unit state
func (s *ServiceResource) Update() error {
	enabled, err := s.unitIsEnabled()
	if err != nil {
		return err
	}

	if s.Enable && !enabled {
		s.enableUnit()
	} else {
		s.disableUnit()
	}

	return s.daemonReload()
}

func init() {
	if util.IsRunningSystemd() {
		Register(serviceResourceType, NewServiceResource)
	}
}
