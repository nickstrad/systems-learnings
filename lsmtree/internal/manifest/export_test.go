package manifest

// Seal exposes seal so tests can build files with a valid checksum around
// any body, to reach the JSON and invariant checks behind it.
var Seal = seal
