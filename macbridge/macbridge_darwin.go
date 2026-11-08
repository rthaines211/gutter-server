//go:build darwin && cgo

// Package macbridge lets a sandboxed Mac app hand its comics folder to this
// server. The app passes bookmark data; we resolve it and start accessing the
// security scope so the scanner can read the folder.
package macbridge

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

static char *cfstr(CFStringRef s) {
	CFIndex n = CFStringGetMaximumSizeForEncoding(CFStringGetLength(s), kCFStringEncodingUTF8) + 1;
	char *b = malloc(n);
	CFStringGetCString(s, b, n, kCFStringEncodingUTF8);
	return b;
}

// Resolves bookmark bytes, starts security-scoped access and returns the path.
// On failure returns NULL and sets *err (caller frees both).
static char *gutter_resolve(const void *data, long len, int scoped, int *started, char **err) {
	CFDataRef d = CFDataCreate(NULL, data, len);
	Boolean stale = false;
	CFErrorRef e = NULL;
	CFURLRef url = CFURLCreateByResolvingBookmarkData(NULL, d,
		scoped ? kCFURLBookmarkResolutionWithSecurityScope : 0, NULL, NULL, &stale, &e);
	CFRelease(d);
	if (!url) {
		CFStringRef s = e ? CFErrorCopyDescription(e) : CFSTR("unknown error");
		*err = cfstr(s);
		if (e) { CFRelease(s); CFRelease(e); }
		return NULL;
	}
	*started = CFURLStartAccessingSecurityScopedResource(url);
	char *p = malloc(4096);
	CFURLGetFileSystemRepresentation(url, true, (UInt8 *)p, 4096);
	// ponytail: url is leaked on purpose; access lasts for the process lifetime.
	return p;
}
*/
import "C"

import (
	"fmt"
	"log"
	"os"
	"unsafe"
)

// Open reads bookmark data from file, resolves it and starts access.
// It tries a security-scoped (app-scope) resolve first, then a plain
// (implicit-scope) one.
func Open(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil || len(data) == 0 {
		return "", fmt.Errorf("macbridge: reading %s: %v", file, err)
	}
	var errs []string
	for _, scoped := range []C.int{1, 0} {
		var started C.int
		var cerr *C.char
		p := C.gutter_resolve(unsafe.Pointer(&data[0]), C.long(len(data)), scoped, &started, &cerr)
		if p == nil {
			errs = append(errs, fmt.Sprintf("scoped=%d: %s", scoped, C.GoString(cerr)))
			C.free(unsafe.Pointer(cerr))
			continue
		}
		path := C.GoString(p)
		C.free(unsafe.Pointer(p))
		_, rerr := os.ReadDir(path)
		log.Printf("macbridge: resolved %s (scoped=%d, startAccessing=%v, readDir err=%v, earlier: %v)", path, scoped, started != 0, rerr, errs)
		return path, nil
	}
	return "", fmt.Errorf("macbridge: resolve failed: %v", errs)
}
