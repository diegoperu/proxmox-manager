// Package pmgrsync embeds the node setup script, so the manager can show its SHA-256 next to the commands that run
// it: the hash comes from the signed binary, and the command on the node refuses to run a different file (D154).
package pmgrsync

import _ "embed"

//go:embed pmgr-sync-setup.sh
var SetupScript []byte
