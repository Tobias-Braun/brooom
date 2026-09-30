package trash

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// newOSTrasher returns the macOS Trash implementation. The home directory is
// only needed by the ~/.Trash fallback, so a missing one is not an error here:
// the fallback reports it for the item that needs it.
func newOSTrasher(opts Options) (Trasher, error) {
	home, _ := os.UserHomeDir()
	return newMacTrash(home), nil
}

// objcBindings holds everything resolved once from the Objective-C runtime.
// purego calls the runtime directly, so the binary stays cgo-free and cross
// compiles from any host with CGO_ENABLED=0.
type objcBindings struct {
	poolPush func() uintptr
	poolPop  func(pool uintptr)

	fileManagerClass objc.Class
	urlClass         objc.Class
	stringClass      objc.Class

	selDefaultManager objc.SEL
	selFileURL        objc.SEL
	selStringUTF8     objc.SEL
	selUTF8String     objc.SEL
	selPath           objc.SEL
	selDescription    objc.SEL
	selTrash          objc.SEL
}

var (
	bindOnce sync.Once
	bindings objcBindings
	bindErr  error
)

// loadBindings loads Foundation and resolves classes and selectors, once.
func loadBindings() (*objcBindings, error) {
	bindOnce.Do(func() {
		if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			bindErr = fmt.Errorf("cannot load Foundation: %w", err)
			return
		}
		lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			bindErr = fmt.Errorf("cannot load the Objective-C runtime: %w", err)
			return
		}
		b := &bindings
		purego.RegisterLibFunc(&b.poolPush, lib, "objc_autoreleasePoolPush")
		purego.RegisterLibFunc(&b.poolPop, lib, "objc_autoreleasePoolPop")
		b.fileManagerClass = objc.GetClass("NSFileManager")
		b.urlClass = objc.GetClass("NSURL")
		b.stringClass = objc.GetClass("NSString")
		if b.fileManagerClass == 0 || b.urlClass == 0 || b.stringClass == 0 {
			bindErr = errors.New("Foundation classes not found")
			return
		}
		b.selDefaultManager = objc.RegisterName("defaultManager")
		b.selFileURL = objc.RegisterName("fileURLWithPath:isDirectory:")
		b.selStringUTF8 = objc.RegisterName("stringWithUTF8String:")
		b.selUTF8String = objc.RegisterName("UTF8String")
		b.selPath = objc.RegisterName("path")
		b.selDescription = objc.RegisterName("localizedDescription")
		b.selTrash = objc.RegisterName("trashItemAtURL:resultingItemURL:error:")
	})
	if bindErr != nil {
		return nil, bindErr
	}
	return &bindings, nil
}

// nativeTrashItem moves path into the Trash with
// -[NSFileManager trashItemAtURL:resultingItemURL:error:] and returns the path
// the item has there, which is what Finder's "Put Back" and brooom's undo need.
//
// The URL is built from the string alone (isDirectory:NO, no stat and no
// symlink resolution), so a symlink is trashed as the link and its target is
// never touched. The call runs inside its own autorelease pool on a locked OS
// thread, as the pool is per thread, and every Foundation string is copied
// into Go memory before the pool is drained.
func nativeTrashItem(path string) (string, error) {
	b, err := loadBindings()
	if err != nil {
		return "", err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := b.poolPush()
	defer b.poolPop(pool)

	cpath := append([]byte(path), 0)
	nsPath := objc.Send[objc.ID](objc.ID(b.stringClass), b.selStringUTF8, &cpath[0])
	if nsPath == 0 {
		return "", fmt.Errorf("cannot trash %q: path is not valid UTF-8", path)
	}
	url := objc.Send[objc.ID](objc.ID(b.urlClass), b.selFileURL, nsPath, false)
	if url == 0 {
		return "", fmt.Errorf("cannot trash %q: cannot build a file URL", path)
	}
	fm := objc.Send[objc.ID](objc.ID(b.fileManagerClass), b.selDefaultManager)

	// Both out-parameters are pointers to object references; the pointees
	// live in Go memory, which does not move, until the call returns.
	var resulting, nsErr objc.ID
	ok := objc.Send[bool](fm, b.selTrash, url, &resulting, &nsErr)
	runtime.KeepAlive(cpath)
	if !ok {
		if nsErr != 0 {
			if msg := goString(b, objc.Send[objc.ID](nsErr, b.selDescription)); msg != "" {
				return "", fmt.Errorf("cannot trash %q: %s", path, msg)
			}
		}
		return "", fmt.Errorf("cannot trash %q: unknown NSFileManager error", path)
	}
	if resulting == 0 {
		return "", fmt.Errorf("cannot trash %q: the system returned no trash location", path)
	}
	stored := goString(b, objc.Send[objc.ID](resulting, b.selPath))
	if stored == "" {
		return "", fmt.Errorf("cannot trash %q: the system returned no trash location", path)
	}
	return stored, nil
}

// goString copies an NSString into a Go string ("" for nil).
func goString(b *objcBindings, s objc.ID) string {
	if s == 0 {
		return ""
	}
	p := objc.Send[*byte](s, b.selUTF8String)
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}
