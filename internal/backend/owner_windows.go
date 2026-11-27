package backend

import "io/fs"

// The Windows temp directory is already per user.
func ownedByCurrentUser(fs.FileInfo) bool { return true }
