//go:build darwin

package main

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// CoreFoundation / Security bindings shared by the keychain and trust settings code
// (purego, no cgo).
// CF objects are represented as uintptr; every object obtained from a Create or Copy
// function must be released with cfRelease.

const (
	cfStringEncodingUTF8  = 0x08000100
	cfNumberSInt32Type    = 3
	errSecSuccess         = 0
	errSecItemNotFound    = -25300
	errSecDuplicateItem   = -25299
	errSecAuthFailed      = -25293
	errSecUserCanceled    = -128
	errAuthorizationDeny  = -60005
	errAuthorizationCancl = -60006
)

var macSec struct {
	once sync.Once
	err  error

	cfRelease               func(uintptr)
	cfStringCreateWithBytes func(uintptr, *byte, int, uint32, bool) uintptr
	cfDataCreate            func(uintptr, *byte, int) uintptr
	cfDataGetBytePtr        func(uintptr) uintptr
	cfDataGetLength         func(uintptr) int
	cfNumberCreate          func(uintptr, int, unsafe.Pointer) uintptr
	cfNumberGetValue        func(uintptr, int, unsafe.Pointer) bool
	cfDictionaryGetValue    func(uintptr, uintptr) uintptr
	cfDictionaryCreate      func(uintptr, *uintptr, *uintptr, int, uintptr, uintptr) uintptr
	cfArrayCreate           func(uintptr, *uintptr, int, uintptr) uintptr
	cfArrayGetCount         func(uintptr) int
	cfArrayGetValueAtIndex  func(uintptr, int) uintptr
	cfGetTypeID             func(uintptr) uintptr
	cfArrayGetTypeID        func() uintptr
	cfDataGetTypeID         func() uintptr

	secItemAdd            func(uintptr, *uintptr) int32
	secItemCopyMatching   func(uintptr, *uintptr) int32
	secItemUpdate         func(uintptr, uintptr) int32
	secItemDelete         func(uintptr) int32
	secCertCreateWithData func(uintptr, uintptr) uintptr
	secCertCopyData       func(uintptr) uintptr
	secPolicyCreateSSL    func(bool, uintptr) uintptr
	secTrustSettingsSet   func(uintptr, uint32, uintptr) int32
	secTrustSettingsRm    func(uintptr, uint32) int32

	dictKeyCallBacks   uintptr
	dictValueCallBacks uintptr
	arrayCallBacks     uintptr
	booleanTrue        uintptr

	kSecClass                    uintptr
	kSecClassGenericPassword     uintptr
	kSecClassCertificate         uintptr
	kSecAttrService              uintptr
	kSecAttrAccount              uintptr
	kSecAttrLabel                uintptr
	kSecAttrAccessible           uintptr
	kSecAttrAccessibleAfterFirst uintptr
	kSecValueData                uintptr
	kSecValueRef                 uintptr
	kSecReturnData               uintptr
	kSecReturnRef                uintptr
	kSecMatchLimit               uintptr
	kSecMatchLimitOne            uintptr
	kSecMatchLimitAll            uintptr
}

func loadMacSecurity() (err error) {
	macSec.once.Do(func() {
		defer func() {
			// RegisterLibFunc panics when a symbol is missing; convert that into an error.
			if r := recover(); r != nil {
				macSec.err = fmt.Errorf("加载 Security 框架失败：%v", r)
			}
		}()
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			macSec.err = err
			return
		}
		sec, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			macSec.err = err
			return
		}
		purego.RegisterLibFunc(&macSec.cfRelease, cf, "CFRelease")
		purego.RegisterLibFunc(&macSec.cfStringCreateWithBytes, cf, "CFStringCreateWithBytes")
		purego.RegisterLibFunc(&macSec.cfDataCreate, cf, "CFDataCreate")
		purego.RegisterLibFunc(&macSec.cfDataGetBytePtr, cf, "CFDataGetBytePtr")
		purego.RegisterLibFunc(&macSec.cfDataGetLength, cf, "CFDataGetLength")
		purego.RegisterLibFunc(&macSec.cfNumberCreate, cf, "CFNumberCreate")
		purego.RegisterLibFunc(&macSec.cfNumberGetValue, cf, "CFNumberGetValue")
		purego.RegisterLibFunc(&macSec.cfDictionaryGetValue, cf, "CFDictionaryGetValue")
		purego.RegisterLibFunc(&macSec.cfDictionaryCreate, cf, "CFDictionaryCreate")
		purego.RegisterLibFunc(&macSec.cfArrayCreate, cf, "CFArrayCreate")
		purego.RegisterLibFunc(&macSec.cfArrayGetCount, cf, "CFArrayGetCount")
		purego.RegisterLibFunc(&macSec.cfArrayGetValueAtIndex, cf, "CFArrayGetValueAtIndex")
		purego.RegisterLibFunc(&macSec.cfGetTypeID, cf, "CFGetTypeID")
		purego.RegisterLibFunc(&macSec.cfArrayGetTypeID, cf, "CFArrayGetTypeID")
		purego.RegisterLibFunc(&macSec.cfDataGetTypeID, cf, "CFDataGetTypeID")

		purego.RegisterLibFunc(&macSec.secItemAdd, sec, "SecItemAdd")
		purego.RegisterLibFunc(&macSec.secItemCopyMatching, sec, "SecItemCopyMatching")
		purego.RegisterLibFunc(&macSec.secItemUpdate, sec, "SecItemUpdate")
		purego.RegisterLibFunc(&macSec.secItemDelete, sec, "SecItemDelete")
		purego.RegisterLibFunc(&macSec.secCertCreateWithData, sec, "SecCertificateCreateWithData")
		purego.RegisterLibFunc(&macSec.secCertCopyData, sec, "SecCertificateCopyData")
		purego.RegisterLibFunc(&macSec.secPolicyCreateSSL, sec, "SecPolicyCreateSSL")
		purego.RegisterLibFunc(&macSec.secTrustSettingsSet, sec, "SecTrustSettingsSetTrustSettings")
		purego.RegisterLibFunc(&macSec.secTrustSettingsRm, sec, "SecTrustSettingsRemoveTrustSettings")

		// Callback structs use the symbol address directly; constants are global variables
		// pointing to CF objects and must be dereferenced.
		macSec.dictKeyCallBacks = mustSym(cf, "kCFTypeDictionaryKeyCallBacks")
		macSec.dictValueCallBacks = mustSym(cf, "kCFTypeDictionaryValueCallBacks")
		macSec.arrayCallBacks = mustSym(cf, "kCFTypeArrayCallBacks")
		macSec.booleanTrue = mustConst(cf, "kCFBooleanTrue")
		for _, c := range []struct {
			dst  *uintptr
			name string
		}{
			{&macSec.kSecClass, "kSecClass"},
			{&macSec.kSecClassGenericPassword, "kSecClassGenericPassword"},
			{&macSec.kSecClassCertificate, "kSecClassCertificate"},
			{&macSec.kSecAttrService, "kSecAttrService"},
			{&macSec.kSecAttrAccount, "kSecAttrAccount"},
			{&macSec.kSecAttrLabel, "kSecAttrLabel"},
			{&macSec.kSecAttrAccessible, "kSecAttrAccessible"},
			{&macSec.kSecAttrAccessibleAfterFirst, "kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly"},
			{&macSec.kSecValueData, "kSecValueData"},
			{&macSec.kSecValueRef, "kSecValueRef"},
			{&macSec.kSecReturnData, "kSecReturnData"},
			{&macSec.kSecReturnRef, "kSecReturnRef"},
			{&macSec.kSecMatchLimit, "kSecMatchLimit"},
			{&macSec.kSecMatchLimitOne, "kSecMatchLimitOne"},
			{&macSec.kSecMatchLimitAll, "kSecMatchLimitAll"},
		} {
			*c.dst = mustConst(sec, c.name)
		}
	})
	return macSec.err
}

func mustSym(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil || p == 0 {
		panic(fmt.Sprintf("missing symbol %s", name))
	}
	return p
}

func mustConst(lib uintptr, name string) uintptr {
	v := *(*uintptr)(cPointer(mustSym(lib, name)))
	if v == 0 {
		panic(fmt.Sprintf("symbol %s is nil", name))
	}
	return v
}

// cPointer converts a C-side address (a symbol or a CF internal buffer) to a pointer;
// that memory is not managed by Go.
func cPointer(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr))
}

// cfBag collects the CF objects created during one call and releases them together in release.
type cfBag []uintptr

func (b *cfBag) keep(ref uintptr) uintptr {
	if ref != 0 {
		*b = append(*b, ref)
	}
	return ref
}

func (b *cfBag) release() {
	for i := len(*b) - 1; i >= 0; i-- {
		macSec.cfRelease((*b)[i])
	}
	*b = nil
}

func (b *cfBag) str(s string) uintptr {
	p := unsafe.StringData(s)
	ref := macSec.cfStringCreateWithBytes(0, p, len(s), cfStringEncodingUTF8, false)
	runtime.KeepAlive(s)
	return b.keep(ref)
}

// data copies v into a new CFData; the caller may then zero its own copy.
func (b *cfBag) data(v []byte) uintptr {
	var p *byte
	if len(v) > 0 {
		p = &v[0]
	}
	ref := macSec.cfDataCreate(0, p, len(v))
	runtime.KeepAlive(v)
	return b.keep(ref)
}

func (b *cfBag) int32(v int32) uintptr {
	return b.keep(macSec.cfNumberCreate(0, cfNumberSInt32Type, unsafe.Pointer(&v)))
}

// dict creates a CFDictionary from alternating key, value arguments; pairs whose value
// is 0 are skipped.
func (b *cfBag) dict(kv ...uintptr) uintptr {
	keys := make([]uintptr, 0, len(kv)/2)
	values := make([]uintptr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i] == 0 || kv[i+1] == 0 {
			continue
		}
		keys = append(keys, kv[i])
		values = append(values, kv[i+1])
	}
	var kp, vp *uintptr
	if len(keys) > 0 {
		kp, vp = &keys[0], &values[0]
	}
	ref := macSec.cfDictionaryCreate(0, kp, vp, len(keys), macSec.dictKeyCallBacks, macSec.dictValueCallBacks)
	runtime.KeepAlive(keys)
	runtime.KeepAlive(values)
	return b.keep(ref)
}

func (b *cfBag) array(items ...uintptr) uintptr {
	var p *uintptr
	if len(items) > 0 {
		p = &items[0]
	}
	ref := macSec.cfArrayCreate(0, p, len(items), macSec.arrayCallBacks)
	runtime.KeepAlive(items)
	return b.keep(ref)
}

// cfDataBytes copies the contents of a CFData into Go memory.
func cfDataBytes(ref uintptr) []byte {
	if ref == 0 || macSec.cfGetTypeID(ref) != macSec.cfDataGetTypeID() {
		return nil
	}
	n := macSec.cfDataGetLength(ref)
	ptr := macSec.cfDataGetBytePtr(ref)
	out := make([]byte, n)
	if n > 0 && ptr != 0 {
		copy(out, unsafe.Slice((*byte)(cPointer(ptr)), n))
	}
	return out
}

// macOSStatusError carries only the OSStatus code and no data.
type macOSStatusError struct {
	op     string
	status int32
}

func (e *macOSStatusError) Error() string {
	return fmt.Sprintf("%s失败（OSStatus %d）", e.op, e.status)
}

func osStatus(op string, status int32) error {
	if status == errSecSuccess {
		return nil
	}
	return &macOSStatusError{op: op, status: status}
}

func osStatusCode(err error) (int32, bool) {
	var e *macOSStatusError
	if errors.As(err, &e) {
		return e.status, true
	}
	return 0, false
}
