//go:build darwin && cgo

package processenv

/*
#include <errno.h>
#include <stdlib.h>
#include <sys/sysctl.h>

static int oro_procargs2(int pid, void *buf, size_t *size) {
	int mib[3] = { CTL_KERN, KERN_PROCARGS2, pid };
	if (sysctl(mib, 3, buf, size, NULL, 0) == -1) {
		return errno;
	}
	return 0;
}
*/
import "C"

import (
	"fmt"
	"syscall"
)

// ReadEntries returns exact environment entries from Darwin's kern.procargs2
// payload. Unlike ps, this source retains NUL boundaries between values.
func ReadEntries(pid int) ([]string, error) {
	raw, err := darwinProcargs(pid)
	if err != nil {
		return nil, fmt.Errorf("read kern.procargs2 for pid %d: %w", pid, err)
	}
	return ParseDarwinEntries(raw)
}

func darwinProcargs(pid int) ([]byte, error) {
	var size C.size_t
	if errno := C.oro_procargs2(C.int(pid), nil, &size); errno != 0 {
		return nil, syscall.Errno(errno)
	}
	if size == 0 {
		return nil, fmt.Errorf("kern.procargs2 returned no data")
	}
	buffer := C.malloc(size)
	if buffer == nil {
		return nil, fmt.Errorf("allocate kern.procargs2 buffer")
	}
	defer C.free(buffer)
	if errno := C.oro_procargs2(C.int(pid), buffer, &size); errno != 0 {
		return nil, syscall.Errno(errno)
	}
	return C.GoBytes(buffer, C.int(size)), nil
}
