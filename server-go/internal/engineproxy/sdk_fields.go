package engineproxy

import (
	"reflect"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"
)

// The engine reads a field under any case, the proxy under one; a key spelled as an API field in
// another case is refused even when empty, since some fields mean something empty (MaskedPaths: []).
// The names come from the API types provisioning's own client sends, so its bodies always pass.
var (
	createRequestNames    = jsonFieldNames(container.CreateRequest{})
	hostConfigNames       = jsonFieldNames(container.HostConfig{})
	networkingConfigNames = jsonFieldNames(network.NetworkingConfig{})
	endpointNames         = jsonFieldNames(network.EndpointSettings{})
	mountNames            = jsonFieldNames(mount.Mount{})
	volumeOptionsNames    = jsonFieldNames(mount.VolumeOptions{})
	tmpfsOptionsNames     = jsonFieldNames(mount.TmpfsOptions{})
	bindOptionsNames      = jsonFieldNames(mount.BindOptions{})
	portBindingNames      = jsonFieldNames(nat.PortBinding{})
	logConfigNames        = jsonFieldNames(container.LogConfig{})
	execNames             = jsonFieldNames(container.ExecOptions{})
	networkCreateNames    = jsonFieldNames(network.CreateRequest{})
	volumeCreateNames     = jsonFieldNames(volume.CreateOptions{})
	connectNames          = jsonFieldNames(network.ConnectOptions{})
	disconnectNames       = jsonFieldNames(network.DisconnectOptions{})
)

// jsonFieldNames lists the keys encoding/json reads into sample's type, embedded structs included.
func jsonFieldNames(sample any) []string {
	return appendFieldNames(nil, reflect.TypeOf(sample))
}

func appendFieldNames(names []string, kind reflect.Type) []string {
	for index := range kind.NumField() {
		field := kind.Field(index)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		embedded := field.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		switch {
		case field.Anonymous && tag == "" && embedded.Kind() == reflect.Struct:
			names = appendFieldNames(names, embedded)
		case !field.IsExported():
		case tag != "":
			names = append(names, tag)
		default:
			names = append(names, field.Name)
		}
	}
	return names
}

// checkObjectSpelling checks the object at value, if it is one, against names.
func checkObjectSpelling(what string, value any, names []string) error {
	object, _ := value.(map[string]any)
	return checkSpelling(what, object, names...)
}

// checkCreateSpelling walks a create body's API objects; the checks after it look fields up by name.
func checkCreateSpelling(create map[string]any) error {
	if err := checkSpelling("container create", create, createRequestNames...); err != nil {
		return err
	}
	host, _ := create["HostConfig"].(map[string]any)
	if err := checkSpelling("HostConfig", host, hostConfigNames...); err != nil {
		return err
	}
	if err := checkObjectSpelling("HostConfig.LogConfig", host["LogConfig"], logConfigNames); err != nil {
		return err
	}
	if err := checkMountsSpelling(host["Mounts"]); err != nil {
		return err
	}
	if err := checkPortBindingsSpelling(host["PortBindings"]); err != nil {
		return err
	}
	networking, _ := create["NetworkingConfig"].(map[string]any)
	if err := checkSpelling("NetworkingConfig", networking, networkingConfigNames...); err != nil {
		return err
	}
	endpoints, _ := networking["EndpointsConfig"].(map[string]any)
	for _, endpoint := range endpoints {
		if err := checkObjectSpelling("endpoint", endpoint, endpointNames); err != nil {
			return err
		}
	}
	return nil
}

func checkMountsSpelling(value any) error {
	mounts, _ := value.([]any)
	for _, entry := range mounts {
		mount, _ := entry.(map[string]any)
		if err := checkSpelling("mount", mount, mountNames...); err != nil {
			return err
		}
		for field, names := range map[string][]string{
			"VolumeOptions": volumeOptionsNames, "TmpfsOptions": tmpfsOptionsNames, "BindOptions": bindOptionsNames,
		} {
			if err := checkObjectSpelling("mount "+field, mount[field], names); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkPortBindingsSpelling(value any) error {
	ports, _ := value.(map[string]any)
	for _, list := range ports {
		bindings, _ := list.([]any)
		for _, binding := range bindings {
			if err := checkObjectSpelling("port binding", binding, portBindingNames); err != nil {
				return err
			}
		}
	}
	return nil
}
