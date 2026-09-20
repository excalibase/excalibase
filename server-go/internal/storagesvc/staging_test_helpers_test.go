package storagesvc

import "strings"

// testUploadID is a deterministic upload id per object key, so a test can
// stage bytes and confirm them without threading an id through every call.
// Real ids are random; only their uniqueness matters to the service.
func testUploadID(key string) string {
	return "upl_" + strings.NewReplacer("/", "_", " ", "_", ".", "_").Replace(key)
}
