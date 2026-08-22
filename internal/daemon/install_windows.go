//go:build windows

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"
)

// ExampleConfig is the config.json template written by install.
const ExampleConfig = `{
  "default": {
    "host": "your-server-host",
    "port": 22,
    "username": "root",
    "password": "your-password",
    "commandWhitelist": [],
    "allowedRemotePaths": ["/tmp", "/home"],
    "commandLogSize": 50,
    "commandLogDir": "logs"
  }
}
`

const vbsContent = `Set WshShell = CreateObject("WScript.Shell")
WshShell.Run """%s"" start --config-file ""%s""", 0, False
`

var (
	ole32 = syscall.MustLoadDLL("ole32.dll")

	procCoInitializeEx  = ole32.MustFindProc("CoInitializeEx")
	procCoCreateInstance = ole32.MustFindProc("CoCreateInstance")
	procCoUninitialize  = ole32.MustFindProc("CoUninitialize")
)

// parseGUID parses a GUID string into syscall.GUID without oleaut32.
func parseGUID(s string) (syscall.GUID, error) {
	if len(s) != 38 || s[0] != '{' || s[37] != '}' {
		return syscall.GUID{}, fmt.Errorf("invalid GUID format: %s", s)
	}
	d1, err := strconv.ParseUint(s[1:9], 16, 32)
	if err != nil {
		return syscall.GUID{}, err
	}
	d2, err := strconv.ParseUint(s[10:14], 16, 16)
	if err != nil {
		return syscall.GUID{}, err
	}
	d3, err := strconv.ParseUint(s[15:19], 16, 16)
	if err != nil {
		return syscall.GUID{}, err
	}
	var d4 [8]byte
	for i := 0; i < 8; i++ {
		start := 20 + i*2
		if i >= 2 {
			start = 21 + i*2
		}
		b, err := strconv.ParseUint(s[start:start+2], 16, 8)
		if err != nil {
			return syscall.GUID{}, err
		}
		d4[i] = byte(b)
	}
	return syscall.GUID{Data1: uint32(d1), Data2: uint16(d2), Data3: uint16(d3), Data4: d4}, nil
}

type iShellLinkWVtbl struct {
	QueryInterface      uintptr
	AddRef              uintptr
	Release             uintptr
	GetPath             uintptr
	GetIDList           uintptr
	SetIDList           uintptr
	GetDescription      uintptr
	SetDescription      uintptr
	GetWorkingDirectory uintptr
	SetWorkingDirectory uintptr
	GetArguments        uintptr
	SetArguments        uintptr
	GetHotkey           uintptr
	SetHotkey           uintptr
	GetShowCmd          uintptr
	SetShowCmd          uintptr
	GetIconLocation     uintptr
	SetIconLocation     uintptr
	SetRelativePath     uintptr
	Resolve             uintptr
	SetPath             uintptr
}

type iShellLinkW struct {
	LpVtbl *iShellLinkWVtbl
}

type iPersistFileVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetClassID     uintptr
	IsDirty        uintptr
	Load           uintptr
	Save           uintptr
	SaveCompleted  uintptr
	GetCurFile     uintptr
}

type iPersistFile struct {
	LpVtbl *iPersistFileVtbl
}

func createShortcut(shortcutPath, targetPath, arguments, workingDir string, windowStyle int) error {
	hr, _, _ := procCoInitializeEx.Call(0, 0)
	if hr != 0 {
		return fmt.Errorf("CoInitializeEx failed: %d", hr)
	}
	defer procCoUninitialize.Call()

	clsid, err := parseGUID("{00021401-0000-0000-C000-000000000046}")
	if err != nil {
		return fmt.Errorf("invalid CLSID: %v", err)
	}
	iid, err := parseGUID("{000214F9-0000-0000-C000-000000000046}")
	if err != nil {
		return fmt.Errorf("invalid IID: %v", err)
	}

	var shellLink *iShellLinkW
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)),
		0,
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iid)),
		uintptr(unsafe.Pointer(&shellLink)),
	)
	if hr != 0 {
		return fmt.Errorf("CoCreateInstance failed: %d", hr)
	}
	defer syscall.SyscallN(shellLink.LpVtbl.Release, uintptr(unsafe.Pointer(shellLink)))

	targetPathPtr, _ := syscall.UTF16PtrFromString(targetPath)
	syscall.SyscallN(shellLink.LpVtbl.SetPath, uintptr(unsafe.Pointer(shellLink)), uintptr(unsafe.Pointer(targetPathPtr)))

	argumentsPtr, _ := syscall.UTF16PtrFromString(arguments)
	syscall.SyscallN(shellLink.LpVtbl.SetArguments, uintptr(unsafe.Pointer(shellLink)), uintptr(unsafe.Pointer(argumentsPtr)))

	workingDirPtr, _ := syscall.UTF16PtrFromString(workingDir)
	syscall.SyscallN(shellLink.LpVtbl.SetWorkingDirectory, uintptr(unsafe.Pointer(shellLink)), uintptr(unsafe.Pointer(workingDirPtr)))

	syscall.SyscallN(shellLink.LpVtbl.SetShowCmd, uintptr(unsafe.Pointer(shellLink)), uintptr(windowStyle))

	persistFileIID, err := parseGUID("{0000010b-0000-0000-C000-000000000046}")
	if err != nil {
		return fmt.Errorf("invalid PersistFile IID: %v", err)
	}
	var persistFile *iPersistFile
	hr, _, _ = syscall.SyscallN(shellLink.LpVtbl.QueryInterface,
		uintptr(unsafe.Pointer(shellLink)),
		uintptr(unsafe.Pointer(&persistFileIID)),
		uintptr(unsafe.Pointer(&persistFile)),
	)
	if hr != 0 {
		return fmt.Errorf("QueryInterface IPersistFile failed: %d", hr)
	}
	defer syscall.SyscallN(persistFile.LpVtbl.Release, uintptr(unsafe.Pointer(persistFile)))

	shortcutPathPtr, _ := syscall.UTF16PtrFromString(shortcutPath)
	syscall.SyscallN(persistFile.LpVtbl.Save,
		uintptr(unsafe.Pointer(persistFile)),
		uintptr(unsafe.Pointer(shortcutPathPtr)),
		1, // TRUE
	)
	return nil
}

// Install sets up Windows autostart: a config template, a VBS launcher and a
// startup folder shortcut that runs the daemon hidden at logon.
func Install() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}
	exePath, _ = filepath.Abs(exePath)
	exeDir := filepath.Dir(exePath)

	configPath := filepath.Join(exeDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(ExampleConfig), 0o644); err != nil {
			return fmt.Errorf("failed to write config.json: %v", err)
		}
		fmt.Println("created config.json")
	} else {
		fmt.Println("config.json already exists")
	}

	vbsPath := filepath.Join(exeDir, "autostart.vbs")
	vbsData := fmt.Sprintf(vbsContent, exePath, configPath)
	if err := os.WriteFile(vbsPath, []byte(vbsData), 0o644); err != nil {
		return fmt.Errorf("failed to write autostart.vbs: %v", err)
	}
	fmt.Println("created autostart.vbs")

	startupDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	shortcutPath := filepath.Join(startupDir, "SSHMCPServer.lnk")
	if err := createShortcut(shortcutPath, "wscript.exe", fmt.Sprintf(`"%s"`, vbsPath), exeDir, 0); err != nil {
		return fmt.Errorf("failed to create shortcut: %v", err)
	}
	fmt.Println("created shortcut in startup folder")
	fmt.Println("installation complete!")
	return nil
}

// Uninstall removes the Windows autostart shortcut.
func Uninstall() error {
	startupDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	shortcutPath := filepath.Join(startupDir, "SSHMCPServer.lnk")
	if _, err := os.Stat(shortcutPath); os.IsNotExist(err) {
		fmt.Println("shortcut not found in startup folder")
		return nil
	}
	if err := os.Remove(shortcutPath); err != nil {
		return fmt.Errorf("failed to remove shortcut: %v", err)
	}
	fmt.Println("removed shortcut from startup folder")
	fmt.Println("uninstallation complete!")
	return nil
}