//go:build windows

package hookinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Security descriptors for directories obot-sentry manages under ProgramData
// and Program Files. Each is applied as the directory is created, so it never
// exists even briefly with the inherited ACL that lets users create entries in
// it, and through a handle when an existing directory is taken over or
// normalized, so it never spreads to children that might be links.
const (
	// machineConfigDirSDDL: SYSTEM and Administrators full control, Users read
	// and execute, inherited by the hook files created inside.
	machineConfigDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"
	// machineConfigFileSDDL is machineConfigDirSDDL for the hook file itself.
	machineConfigFileSDDL = "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x1200a9;;;BU)"
	// runDirSDDL: SYSTEM and Administrators only. The run log lists every
	// user's paths and errors, and a readable file is one a user could hold an
	// oplock on to stall the writer.
	runDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	// sharedParentSDDL is ProgramData's own default — users may create
	// folders and files, and own what they create — but owned by
	// Administrators. It is what a directory above a managed one (obot,
	// OpenAI) gets when it was created by a user, who as its owner could
	// otherwise move the managed directory out from under it between runs.
	sharedParentSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICIIO;FA;;;CO)(A;OICI;0x1200a9;;;BU)(A;CI;0x116;;;BU)"
)

// trustedInstallerSID owns the system's own directories, Program Files among
// them.
const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// trustedDirOwner reports whether owner is SYSTEM, Administrators,
// TrustedInstaller, or an account in the local Administrators group. An
// administrator's own account owns what it creates when UAC is off, or under the
// "object creator" owner policy; what such a directory holds is the
// administrator's and is kept.
func trustedDirOwner(owner *windows.SID) bool {
	if trustedExecutableOwner(owner) {
		return true
	}
	if owner == nil {
		return false
	}
	if strings.EqualFold(owner.String(), trustedInstallerSID) {
		return true
	}
	return localAdministrators()[owner.String()]
}

var (
	modNetapi32                 = windows.NewLazySystemDLL("netapi32.dll")
	procNetLocalGroupGetMembers = modNetapi32.NewProc("NetLocalGroupGetMembers")
)

// localAdministrators returns the SIDs of the direct members of the local
// Administrators group, looked up once. The group's name is localized, so it is
// found from its well-known SID.
var localAdministrators = sync.OnceValue(func() map[string]bool {
	members := map[string]bool{}
	ba, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return members
	}
	name, _, _, err := ba.LookupAccount("")
	if err != nil {
		return members
	}
	group, err := windows.UTF16PtrFromString(name)
	if err != nil || procNetLocalGroupGetMembers.Find() != nil {
		return members
	}
	var (
		buf           *byte
		read, total   uint32
		resume        uintptr
		maxPreferred  = uintptr(0xFFFFFFFF)
		levelSIDsOnly = uintptr(0)
	)
	r, _, _ := procNetLocalGroupGetMembers.Call(0, uintptr(unsafe.Pointer(group)), levelSIDsOnly,
		uintptr(unsafe.Pointer(&buf)), maxPreferred,
		uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
	if r != 0 || buf == nil {
		return members
	}
	defer func() { _ = windows.NetApiBufferFree(buf) }()
	// LOCALGROUP_MEMBERS_INFO_0 is a single PSID.
	for _, sid := range unsafe.Slice((**windows.SID)(unsafe.Pointer(buf)), read) {
		if sid != nil {
			members[(*sid).String()] = true
		}
	}
	return members
})

// enableTakeOwnership turns on SeRestorePrivilege and SeTakeOwnershipPrivilege,
// which SYSTEM and elevated administrators hold but have disabled, so a
// directory whose ACL shuts them out can still be opened to take it over.
var enableTakeOwnership = sync.OnceValue(func() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer func() { _ = token.Close() }()
	for _, name := range []string{"SeRestorePrivilege", "SeTakeOwnershipPrivilege"} {
		var luid windows.LUID
		p, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return err
		}
		if err := windows.LookupPrivilegeValue(nil, p, &luid); err != nil {
			return err
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		if err := windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil); err != nil {
			return err
		}
	}
	return nil
})

// heldDirs is a set of directory handles kept open without delete sharing, so
// none of the directories can be renamed or removed — and so none can be
// replaced by a junction — until release.
type heldDirs struct{ handles []windows.Handle }

func (h *heldDirs) release() {
	for _, x := range h.handles {
		_ = windows.CloseHandle(x)
	}
	h.handles = nil
}

// openDirAsItself opens path with access, without following a reparse point and
// denying delete sharing, and reports its attributes and owner.
func openDirAsItself(path string, access uint32) (windows.Handle, uint32, *windows.SID, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, nil, err
	}
	h, err := windows.CreateFile(name, access|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, 0, nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return 0, 0, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, 0, nil, fmt.Errorf("reading the owner of %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, 0, nil, fmt.Errorf("reading the owner of %s: %w", path, err)
	}
	return h, info.FileAttributes, owner, nil
}

// chainOptions says how holdDirChain treats the directories it holds.
type chainOptions struct {
	// manage creates missing directories, takes over ones a user owns, and
	// normalizes the last one's ACL. Without it the chain is only checked and
	// held (a user's profile path, where nothing is ours to change).
	manage bool
	// create allows creating missing directories and taking over untrusted
	// ones; without it (uninstall, reading the record) they are refused.
	create bool
	// leafSDDL and midSDDL are the descriptors for the last directory and the
	// ones above it.
	leafSDDL, midSDDL string
}

// holdDirChain holds every directory from base (exclusive) down to dir,
// adding the handles to held, and reports whether dir was taken over from an
// untrusted owner — in which case the caller should empty it, since what is in
// it was put there by a user.
//
// Every directory must be a directory and not a reparse point. With manage set,
// a reparse point is moved aside (renaming moves a link itself, never what it
// points to) and the directory recreated; a directory owned by anyone other than
// SYSTEM, Administrators, TrustedInstaller, or a local administrator is taken
// over in place, through its handle: its owner becomes Administrators and its
// ACL the chain's descriptor. Nothing is renamed but links, so a user cannot
// block a takeover by holding the directory open, and a directory an
// administrator owns, with its contents, is kept.
func holdDirChain(base, dir string, opts chainOptions, held *heldDirs) (bool, error) {
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false, fmt.Errorf("%s is not beneath %s", dir, base)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := base
	reclaimed := false
	for idx, part := range parts {
		cur = filepath.Join(cur, part)
		leaf := idx == len(parts)-1
		sddl := opts.midSDDL
		if leaf {
			sddl = opts.leafSDDL
		}
		h, took, err := holdOneDir(cur, leaf, sddl, opts)
		if err != nil {
			return false, err
		}
		held.handles = append(held.handles, h)
		reclaimed = leaf && took
	}
	return reclaimed, nil
}

func holdOneDir(path string, leaf bool, sddl string, opts chainOptions) (windows.Handle, bool, error) {
	var access uint32
	if opts.manage && opts.create {
		access = windows.WRITE_DAC | windows.WRITE_OWNER
	}
	for attempt := 0; attempt < 3; attempt++ {
		h, attrs, owner, err := openDirAsItself(path, access)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if !opts.manage || !opts.create {
				return 0, false, err
			}
			if err := createDirWithSDDL(path, sddl); err != nil && !errors.Is(err, fs.ErrExist) {
				return 0, false, err
			}
			continue
		case errors.Is(err, windows.ERROR_ACCESS_DENIED) && access != 0 && attempt == 0:
			// A directory whose ACL shuts out SYSTEM: open it with the
			// privileges that bypass the ACL, then take it over.
			if perr := enableTakeOwnership(); perr != nil {
				return 0, false, fmt.Errorf("%w (enabling take-ownership: %v)", err, perr)
			}
			continue
		case err != nil:
			return 0, false, err
		}

		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			_ = windows.CloseHandle(h)
			if !opts.manage || !opts.create {
				return 0, false, fmt.Errorf("refusing %s: not a plain directory", path)
			}
			aside := fmt.Sprintf("%s.untrusted-%d", path, time.Now().UnixNano())
			if err := os.Rename(path, aside); err != nil {
				return 0, false, fmt.Errorf("moving aside %s, which is not a plain directory: %w", path, err)
			}
			if err := createDirWithSDDL(path, sddl); err != nil && !errors.Is(err, fs.ErrExist) {
				return 0, false, err
			}
			continue
		}

		if !opts.manage {
			return h, false, nil
		}
		trusted := trustedDirOwner(owner)
		switch {
		case !trusted && !opts.create:
			_ = windows.CloseHandle(h)
			return 0, false, fmt.Errorf("refusing %s: not owned by SYSTEM or Administrators", path)
		case !trusted:
			if err := setSecurityOnHandle(h, sddl, true); err != nil {
				_ = windows.CloseHandle(h)
				return 0, false, fmt.Errorf("taking over %s: %w", path, err)
			}
			return h, true, nil
		case leaf && opts.create:
			// Ours already: make sure its ACL is the current one, which an
			// earlier release may have set more loosely.
			if err := setSecurityOnHandle(h, sddl, false); err != nil {
				_ = windows.CloseHandle(h)
				return 0, false, fmt.Errorf("securing %s: %w", path, err)
			}
		}
		return h, false, nil
	}
	return 0, false, fmt.Errorf("could not secure %s", path)
}

// setSecurityOnHandle applies sddl's DACL, and its owner when withOwner is set,
// to the object behind h. SetKernelObjectSecurity sets exactly that object's
// descriptor and does not propagate to children, any of which might be a link.
func setSecurityOnHandle(h windows.Handle, sddl string, withOwner bool) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if withOwner {
		info |= windows.OWNER_SECURITY_INFORMATION
	}
	return windows.SetKernelObjectSecurity(h, info, sd)
}

// createDirWithSDDL creates path with the security descriptor sddl, or with
// the inherited one when sddl is empty. If the token may not name
// Administrators as owner, it is created with the default owner instead.
func createDirWithSDDL(path, sddl string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if sddl == "" {
		if err := windows.CreateDirectory(name, nil); err != nil {
			return &fs.PathError{Op: "mkdir", Path: path, Err: err}
		}
		return nil
	}
	for _, d := range []string{sddl, strings.TrimPrefix(sddl, "O:BA")} {
		sd, err := windows.SecurityDescriptorFromString(d)
		if err != nil {
			return fmt.Errorf("building the security descriptor for %s: %w", path, err)
		}
		sa := windows.SecurityAttributes{SecurityDescriptor: sd}
		sa.Length = uint32(unsafe.Sizeof(sa))
		err = windows.CreateDirectory(name, &sa)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_INVALID_OWNER) {
			return &fs.PathError{Op: "mkdir", Path: path, Err: err}
		}
	}
	return fmt.Errorf("creating %s: no usable owner", path)
}

// emptyDir removes everything in the held directory dir through a handle to
// it. os.Root removes links as themselves, never what they point to.
func emptyDir(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		if err := root.RemoveAll(n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// machineBaseFor returns the trusted known folder dir lives beneath —
// ProgramData or Program Files — or its volume root.
func machineBaseFor(dir string) string {
	bases := []string{windowsProgramData(), `C:\Program Files`}
	if pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT); err == nil {
		bases = append(bases, pf)
	}
	for _, b := range bases {
		if strings.HasPrefix(strings.ToLower(dir), strings.ToLower(strings.TrimRight(b, `\`))+`\`) {
			return b
		}
	}
	return filepath.VolumeName(dir) + `\`
}

// lockMachineDirs secures and holds the directories of the machine-wide
// destinations: ProgramData lets any user create directories, so the folder a
// hook file goes in, or one above it, could otherwise be a junction a user
// planted to send SYSTEM's writes elsewhere, or a folder a user made first and
// still controls. A folder taken over from a user is emptied: what is in it is
// theirs, not an administrator's policy. When create is false (uninstall) a
// missing directory just means there is nothing to remove.
func lockMachineDirs(dests []Destination, create bool) (map[string]error, func()) {
	held := &heldDirs{}
	errs := map[string]error{}
	opts := chainOptions{manage: true, create: create, leafSDDL: machineConfigDirSDDL, midSDDL: sharedParentSDDL}
	for _, d := range dests {
		if d.Scope != ScopeMachine || d.Abs == "" {
			continue
		}
		dir := filepath.Dir(d.Abs)
		reclaimed, err := holdDirChain(machineBaseFor(dir), dir, opts, held)
		switch {
		case err != nil && !create && errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			errs[d.Abs] = err
		case reclaimed:
			if err := emptyDir(dir); err != nil {
				errs[d.Abs] = fmt.Errorf("emptying %s, taken over from a user: %w", dir, err)
			}
		}
	}
	return errs, held.release
}
