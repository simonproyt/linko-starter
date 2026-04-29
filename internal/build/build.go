package build

// Build-time variables. Overridden with -ldflags during build.
var (
	GitSHA    = "unknown"
	BuildTime = "unknown"
)
