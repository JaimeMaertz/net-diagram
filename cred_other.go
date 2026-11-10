//go:build !windows

package netdiagram

import "errors"

var errNoCredStore = errors.New("saved logins need Windows Credential Manager")

// ReadCredential needs Windows Credential Manager.
func ReadCredential(target string) (user, password string, err error) {
	return "", "", errNoCredStore
}

// WriteCredential needs Windows Credential Manager.
func WriteCredential(target, user, password string) error { return errNoCredStore }
