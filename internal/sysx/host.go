package sysx

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type OS struct {
	ID        string `json:"id"`
	VersionID string `json:"version_id"`
	Codename  string `json:"codename"`
	Pretty    string `json:"pretty"`
}

func (o OS) Debian() bool {
	return o.ID == "debian"
}

func (o OS) Ubuntu() bool {
	return o.ID == "ubuntu"
}

func ReadOS() OS {
	var o OS
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return o
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			o.ID = v
		case "VERSION_ID":
			o.VersionID = v
		case "VERSION_CODENAME":
			o.Codename = v
		case "PRETTY_NAME":
			o.Pretty = v
		}
	}
	return o
}

func KernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

func ReadString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func ReadInt(path string) (int, bool) {
	v, err := strconv.Atoi(ReadString(path))
	return v, err == nil
}

func Sysctl(name string) string {
	return ReadString("/proc/sys/" + strings.ReplaceAll(name, ".", "/"))
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func IsRoot() bool {
	return os.Geteuid() == 0
}
