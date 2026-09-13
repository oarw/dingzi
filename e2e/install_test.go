package e2e

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise the real installer with local downloads and an isolated filesystem.
// No service manager, network request, or system installation is involved.
func TestInstallPreservesWorkingBinaryOnFailure(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX sh is not installed")
	}
	source, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		configure bool
		checksum  bool
	}{
		{"configuration rejected", false, true},
		{"checksum rejected", true, false},
		{"successful upgrade", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, sub := range []string{"bin", "config", "mocks", "downloads", "tmp"} {
				if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			binary := "#!/bin/sh\nexit 42\n"
			if tc.configure {
				binary = `#!/bin/sh
[ "$1" = --configure ] || exit 43
shift
while [ "$#" -gt 0 ]; do
  case "$1" in
    --config) config=$2; shift 2 ;;
    --server|--secret) shift 2 ;;
    --allow-terminal=*) shift ;;
    *) exit 44 ;;
  esac
done
printf 'configured\n' > "$config"
`
			}
			write("downloads/dingzi-agent", binary)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(binary)))
			if !tc.checksum {
				hash = strings.Repeat("0", 64)
			}
			write("downloads/checksums.txt", hash+"  dingzi-agent-linux-amd64\n")
			write("bin/dingzi-agent", "previous binary\n")
			write("config/agent.yaml", "previous config\n")
			write("mocks/id", "#!/bin/sh\nprintf '0\\n'\n")
			write("mocks/uname", "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; esac\n")
			if runtime.GOOS == "windows" {
				// Git's install cannot reliably chmod directories created by Go on
				// NTFS. Keep real file installation; only bypass directory modes.
				write("mocks/install", `#!/bin/sh
if [ "$1" = -d ]; then
  shift
  [ "$1" = -m ] && shift 2
  mkdir -p "$@"
else
  exec /usr/bin/install "$@"
fi
`)
			}
			write("mocks/curl", `#!/bin/sh
[ "$1" = -fsSL ] && [ "$3" = -o ] || exit 45
case "$2" in
  */checksums.txt) cp "$DINGZI_INSTALL_TEST/downloads/checksums.txt" "$4" ;;
  */dingzi-agent-linux-amd64) cp "$DINGZI_INSTALL_TEST/downloads/dingzi-agent" "$4" ;;
  *) exit 46 ;;
esac
`)
			script := string(source)
			for from, to := range map[string]string{
				`BIN_DIR="/usr/local/bin"`: `BIN_DIR="$DINGZI_INSTALL_TEST/bin"`,
				`CONF_DIR="/etc/dingzi"`:   `CONF_DIR="$DINGZI_INSTALL_TEST/config"`,
				`INIT="$(detect_init)"`:    `INIT=none`,
			} {
				if strings.Count(script, from) != 1 {
					t.Fatalf("cannot isolate installer setting %s", from)
				}
				script = strings.Replace(script, from, to, 1)
			}
			// Git for Windows supplies sh and cygpath; Linux uses the path as-is.
			setup := `if command -v cygpath >/dev/null 2>&1; then
  DINGZI_INSTALL_TEST=$(cygpath -u "$DINGZI_INSTALL_TEST")
fi
export DINGZI_INSTALL_TEST
export PATH="$DINGZI_INSTALL_TEST/mocks:$PATH"
export TMPDIR="$DINGZI_INSTALL_TEST/tmp"
`
			write("install.sh", setup+script)
			cmd := exec.Command(sh, filepath.ToSlash(filepath.Join(dir, "install.sh")), "--version", "v-test", "--server", "https://panel.invalid", "--secret", "local-fixture")
			cmd.Env = append(os.Environ(), "DINGZI_INSTALL_TEST="+filepath.ToSlash(dir))
			output, err := cmd.CombinedOutput()
			success := tc.configure && tc.checksum
			if (err == nil) != success {
				t.Fatalf("installer success=%v, want %v: %v\n%s", err == nil, success, err, output)
			}
			installed, err := os.ReadFile(filepath.Join(dir, "bin", "dingzi-agent"))
			if err != nil {
				t.Fatal(err)
			}
			config, err := os.ReadFile(filepath.Join(dir, "config", "agent.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if success {
				if string(installed) != binary || string(config) != "configured\n" {
					t.Fatal("successful installation did not activate the configured binary")
				}
			} else if string(installed) != "previous binary\n" || string(config) != "previous config\n" {
				t.Fatal("failed upgrade replaced the working binary or configuration")
			}
			entries, err := os.ReadDir(filepath.Join(dir, "bin"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("installer left staged executables: %v %v", entries, err)
			}
		})
	}
}
