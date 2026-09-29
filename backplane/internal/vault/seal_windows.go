//go:build windows

package vault

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiEntropy binds sealed blobs to Backplane so another DPAPI consumer on
// the same account cannot casually unseal them.
var dpapiEntropy = []byte("safisolutions.backplane.vault.v1")

type dpapiSealer struct{}

func newSealer(dir string) (sealer, error) { return dpapiSealer{}, nil }

func (dpapiSealer) Scheme() string { return "windows-dpapi" }

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func takeBlob(out *windows.DataBlob) []byte {
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size == 0 {
		return nil
	}
	res := make([]byte, out.Size)
	copy(res, unsafe.Slice(out.Data, out.Size))
	return res
}

const cryptprotectUIForbidden = 0x1

func (dpapiSealer) Seal(plain []byte) ([]byte, error) {
	var out windows.DataBlob
	name, _ := windows.UTF16PtrFromString("Backplane secret")
	if err := windows.CryptProtectData(blob(plain), name, blob(dpapiEntropy), 0, nil, cryptprotectUIForbidden, &out); err != nil {
		return nil, err
	}
	return takeBlob(&out), nil
}

func (dpapiSealer) Open(sealed []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(sealed), nil, blob(dpapiEntropy), 0, nil, cryptprotectUIForbidden, &out); err != nil {
		return nil, err
	}
	return takeBlob(&out), nil
}
