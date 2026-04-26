// Command velox-cli is the operator CLI for a Velox deployment. It
// talks to the same /v1/* HTTP surface any external integration uses —
// no direct DB coupling — so the CLI is a faithful proxy for "what
// could a customer/integrator do via the API right now?".
//
// Auth: a platform API key from the VELOX_API_KEY env var or the
// --api-key flag. The CLI never writes the key to disk.
package main

import (
	"fmt"
	"os"

	"github.com/sagarsuperuser/velox/cmd/velox-cli/cmd"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		// cobra already prints the error + usage; we just want the
		// exit code to reflect it so shell pipelines fail visibly.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
