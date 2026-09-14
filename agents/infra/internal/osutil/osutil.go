// Package osutil holds small OS differences shared by several packages (open flags, file identity,
// opening files that other processes may rename or delete). Linux and macOS use the POSIX
// behaviour; Windows gets equivalents (D-104).
package osutil
