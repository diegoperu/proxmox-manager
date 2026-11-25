// Package pmgrscripts embeds the template and snippet tooling in the manager binary, so the files the manager
// delivers to the nodes are exactly the ones of the signed release (D150). Tests are not embedded.
package pmgrscripts

import "embed"

//go:embed bin lib os.d profiles snippets.d helpers README.md cluster.conf.example update.sh update_all.sh update_debian_template.sh update_rocky_template.sh update_ubuntu_template.sh
var FS embed.FS
