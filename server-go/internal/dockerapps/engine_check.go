package dockerapps

import "github.com/docker/docker/client"

var _ Engine = (*client.Client)(nil)
