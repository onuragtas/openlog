// Command terraform-provider-openlog serves the openlog Terraform provider over the plugin protocol
// (docs/operations/terraform.md). Terraform starts it; running it by hand only makes sense with -debug, which
// prints the TF_REATTACH_PROVIDERS line a locally built provider is attached with.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/onuragtas/openlog/terraform/internal/provider"
)

// version is stamped at build time (-ldflags "-X main.version=…") and travels in the User-Agent of every API
// request, as for the other openlog binaries.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider for a debugger and print its TF_REATTACH_PROVIDERS line")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		// The address a Terraform configuration names in required_providers.
		Address: "registry.terraform.io/onuragtas/openlog",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
