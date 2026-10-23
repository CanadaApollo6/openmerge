package audit

import (
	"syscall"
	"testing"
)

func TestPublicationSanitizedUnsupportedFilesystem(t *testing.T) {
	for _, errno := range []error{syscall.EINVAL, syscall.ENOSYS, syscall.EOPNOTSUPP, errUnsupported} {
		if e := publicationError(errno); e != errUnsupported {
			t.Fatal("unsupported filesystem misclassified")
		}
	}
	if publicationError(syscall.EEXIST) != errOutput {
		t.Fatal("target race misclassified")
	}
}
