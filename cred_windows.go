//go:build windows

package netdiagram

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	advapi32      = syscall.NewLazyDLL("advapi32.dll")
	procCredRead  = advapi32.NewProc("CredReadW")
	procCredWrite = advapi32.NewProc("CredWriteW")
	procCredFree  = advapi32.NewProc("CredFree")
)

// WriteCredential saves a generic credential, as cmdkey /generic would: the password as UTF-16,
// kept for this user on this machine.
func WriteCredential(target, user, password string) error {
	t, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	u, err := syscall.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	pw := utf16.Encode([]rune(password))
	blob := make([]byte, 2*len(pw))
	for i, c := range pw {
		blob[2*i], blob[2*i+1] = byte(c), byte(c>>8)
	}
	c := credentialW{Type: 1 /* CRED_TYPE_GENERIC */, TargetName: t, UserName: u, Persist: 2 /* CRED_PERSIST_LOCAL_MACHINE */}
	c.CredentialBlobSize = uint32(len(blob))
	if len(blob) > 0 {
		c.CredentialBlob = &blob[0]
	}
	r, _, e := procCredWrite.Call(uintptr(unsafe.Pointer(&c)), 0)
	if r == 0 {
		return fmt.Errorf("saving login %q: %v", target, e)
	}
	return nil
}

type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// ReadCredential returns the user and password saved in Windows Credential Manager as a
// generic credential (cmdkey /generic:target). The password is UTF-16 as cmdkey stores it.
func ReadCredential(target string) (user, password string, err error) {
	t, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return "", "", err
	}
	var c *credentialW
	r, _, e := procCredRead.Call(uintptr(unsafe.Pointer(t)), 1 /* CRED_TYPE_GENERIC */, 0, uintptr(unsafe.Pointer(&c)))
	if r == 0 {
		return "", "", fmt.Errorf("no saved login %q: %v", target, e)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(c)))
	if c.UserName != nil {
		user = syscall.UTF16ToString((*[1 << 16]uint16)(unsafe.Pointer(c.UserName))[:])
	}
	n := int(c.CredentialBlobSize)
	if n > 0 && c.CredentialBlob != nil {
		blob := unsafe.Slice(c.CredentialBlob, n)
		u := make([]uint16, n/2)
		for i := range u {
			u[i] = uint16(blob[2*i]) | uint16(blob[2*i+1])<<8
		}
		password = string(utf16.Decode(u))
	}
	return user, password, nil
}
