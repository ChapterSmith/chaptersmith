package audio

import "os"

// pinFile is unnecessary on Windows: file identity there includes a sequence
// number that changes when an index is reused, and a second handle would stop
// the caller from replacing its own output.
func pinFile(*os.File) (release func()) { return func() {} }
