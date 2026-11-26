package routepolicy

// Reasons a write route resolves, is exempt from, or is unrelated to the
// owner's trust (EXC-899). They are named so the route tables stay readable
// and a reason repeated across routes is written once.
const (
	whySitesExemptInV1        = "exempt in v1: site builds run on our own infrastructure (gVisor, behind the build egress proxy) and server-rendered sites run Workers under our Cloudflare account, so the app edge limits do not reach them; egress for builds and Workers is the EXC-844 follow-up"
	whyGitSourceOnly          = "stores or removes the app's git source, or starts a git provider sign-in; renders nothing"
	whyNeverRefusedForTrust   = "resuming or unsuspending is never refused for trust; the workload is rendered with the owner's class"
	whyFunctionNoNewReach     = "removes a function or stores a secret; no new reach"
	whyFunctionCode           = "creates or runs function code, or sets its egress list; a limited owner gets an empty allow-list"
	whyRendersWorkload        = "renders the workload and its ingress"
	whyInvokeDeployedFunction = "invoking a deployed function; its egress list was decided when it was deployed"
	whyLogDrain               = "exempt in v1: forwards the project's logs to the owner's own endpoint from our log-forwarder; outbound limits for it are a follow-up"
	whyAuthHook               = "exempt in v1: our auth service calls the owner's own endpoints; not an app behind the edge, outbound limits for hooks are a follow-up"
	whyDatabasePort           = "exempt in v1: the public database port and its allow-list; database traffic does not pass the HTTP edge, limits for it are a follow-up"
	whyS3Keys                 = "exempt in v1: S3 access keys for the project's storage; storage egress limits are a follow-up"
	whySchedulerRuntime       = "exempt in v1: runtime-token call that schedules the owner's functions; their egress list was decided at deploy"
)
