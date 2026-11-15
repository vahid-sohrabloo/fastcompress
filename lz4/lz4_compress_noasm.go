//go:build !amd64 || appengine || noasm || purego || !gc

package lz4

func (c *streamInternal) compressNoDictAsm(src, dst []byte, maxOutputSize int, od outputDirective, tt tableType, di dictIssue, acceleration int) (int, bool) {
	return 0, false
}

func (c *streamInternal) compressPrefixAsm(win []byte, srcStart int, dst []byte, maxOutputSize int, od outputDirective, di dictIssue, acceleration int) (int, bool) {
	return 0, false
}

func (c *streamInternal) compressResumeAsm(src, dst []byte, dstCapacity int, st *resumeState, acceleration int) (int, bool) {
	return 0, false
}

func (c *streamInternal) compressExtDictAsm(src, dst []byte, dstCapacity int, od outputDirective, di dictIssue,
	acceleration int, suspend *resumeState) (int, bool) {
	return 0, false
}
