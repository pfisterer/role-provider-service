package webserver

import (
	"os"
	"strings"

	"github.com/pfisterer/role-provider-service/internal/generated_docs"
)

// appVersion is the version this process reports (index page): APP_VERSION, which the Helm chart sets from the chart's appVersion — the tag the image is deployed under —, else the embedded VERSION file. The embedded file alone is not enough: a stable release retags the image of the pre-release before it without rebuilding, so the binary carries the pre-release's number.
var appVersion = runningVersion(os.Getenv("APP_VERSION"), generated_docs.Version)

// runningVersion is env when set, else the embedded version; both trimmed (the VERSION file ends in a newline).
func runningVersion(env, embedded string) string {
	if v := strings.TrimSpace(env); v != "" {
		return v
	}
	return strings.TrimSpace(embedded)
}
