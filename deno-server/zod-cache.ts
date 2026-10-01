// Cached into the image (Dockerfile): the zod a function imports for its
// args (`npm:zod@^3.22.0`, or `zod` through the import map) must load with
// no network, since a project runtime has none unless its allowlist says so.
import "npm:zod@^3.22.0";
