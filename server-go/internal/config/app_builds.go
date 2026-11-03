package config

// The images an app build (EXC-547) runs, pinned by digest so a moved tag never
// changes what builds a user's code. Chosen and measured in the gVisor
// app-build spike (excalibase-knowledge docs/spikes/2026-10-29-gvisor-app-builds.md);
// bump them deliberately.
const (
	// DefaultAppBuildKitImage builds Dockerfiles: rootless BuildKit, run daemonless inside the build pod.
	DefaultAppBuildKitImage = "moby/buildkit:v0.34.0-rootless@sha256:65eca3c01cd1180c8f64565c4e3c5983af5145818b0e6fa863385d099eb73855"
	// DefaultAppBuilderImage is the Paketo builder whose lifecycle runs each Buildpacks phase.
	DefaultAppBuilderImage = "paketobuildpacks/ubuntu-noble-builder:0.0.203@sha256:ba79c24b55830210fc5ee06768237ab989dbaeec36adb60a986bcb8e70e4d7cd"
	// DefaultAppRunImage is the image Buildpacks builds onto, the builder's own stack.
	DefaultAppRunImage = "docker.io/paketobuildpacks/ubuntu-noble-run:0.0.138@sha256:3af16be0ed95c43c46a92e81b95ab68544ef8169c6be6026169889ad5c6d6ef0"
	// DefaultAppCraneImage fetches the run image into the build's OCI layout.
	DefaultAppCraneImage = "gcr.io/go-containerregistry/crane:v0.22.1@sha256:1f968817b95790bed063f71175aa6b8ff879fa17064020415f3e18bb6e6a36e1"
)
