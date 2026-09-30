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

// Run the shipped script with fake downloads/service managers. All writes stay
// inside t.TempDir, including the service definitions and persistent data.
func TestPanelInstaller(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX sh is not installed")
	}
	source, err := os.ReadFile("../install-server.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, init, failure string
		args                []string
		fresh               bool
		linkedData          bool
	}{
		{name: "systemd fresh install", init: "systemd", fresh: true},
		{name: "systemd upgrade preserves settings", init: "systemd"},
		{name: "openrc fresh install", init: "openrc", fresh: true},
		{name: "openrc upgrade preserves settings", init: "openrc"},
		{name: "upgrade leaves linked data children untouched", init: "systemd", linkedData: true},
		{name: "explicit settings override", init: "systemd", args: []string{"--listen", "[::1]:9009", "--secure-cookie=false", "--retention-days", "7"}},
		{name: "openrc IPv6 settings", init: "openrc", args: []string{"--listen", "[::1]:9009", "--secure-cookie=false", "--retention-days", "7"}},
		{name: "checksum failure", init: "systemd", failure: "checksum"},
		{name: "duplicate checksum", init: "systemd", failure: "duplicate"},
		{name: "binary cannot execute", init: "systemd", failure: "binary"},
		{name: "missing release asset", init: "systemd", failure: "download"},
		{name: "invalid settings are not executed", init: "systemd", failure: "config"},
		{name: "invalid port", init: "systemd", failure: "arguments", args: []string{"--listen", ":80"}},
		{name: "invalid retention", init: "systemd", failure: "arguments", args: []string{"--retention-days", "366"}},
		{name: "invalid version", init: "systemd", failure: "arguments", args: []string{"--version", "../../latest"}},
		{name: "enable failure is reported", init: "systemd", failure: "enable"},
		{name: "startup failure is reported", init: "systemd", failure: "restart"},
		{name: "crash after start is reported", init: "systemd", failure: "is-active"},
		{name: "openrc startup failure is reported", init: "openrc", failure: "restart"},
		{name: "systemd uninstall preserves data", init: "systemd", args: []string{"--uninstall"}},
		{name: "openrc uninstall preserves data", init: "openrc", args: []string{"--uninstall"}},
		{name: "stop failure prevents uninstall", init: "systemd", failure: "stop", args: []string{"--uninstall"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, sub := range []string{"bin", "config", "data", "systemd", "init", "log", "mocks", "downloads", "tmp"} {
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
			read := func(path string) string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			binary := "#!/bin/sh\n[ \"$1\" = --version ] || exit 43\n[ \"$DINGZI_TEST_FAILURE\" != binary ] || exit 42\necho dingzi-server v1.0.0\n"
			write("downloads/server", binary)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(binary)))
			if tc.failure == "checksum" {
				hash = strings.Repeat("0", 64)
			}
			checksum := hash + "  dingzi-server-linux-amd64\n"
			if tc.failure == "duplicate" {
				checksum += checksum
			}
			write("downloads/checksums.txt", checksum)
			previousConfig := "LISTEN=127.0.0.1:9000\nSECURE_COOKIE=true\nRETENTION_DAYS=90\n"
			if tc.failure == "config" {
				previousConfig = "LISTEN=$(touch \"$DINGZI_INSTALL_TEST/executed\")\n"
			}
			servicePath := "systemd/dingzi-server.service"
			if tc.init == "openrc" {
				servicePath = "init/dingzi-server"
			}
			if !tc.fresh {
				write("bin/dingzi-server", "previous binary\n")
				write("config/server.conf", previousConfig)
				write("data/config.yaml", "persisted credentials\n")
				write("data/dingzi.db", "persisted database\n")
				write(servicePath, "previous service\n")
			}
			if tc.linkedData {
				if runtime.GOOS == "windows" {
					t.Skip("checks POSIX link and mode semantics")
				}
				write("outside-marker", "unchanged\n")
				if err := os.Chmod(filepath.Join(dir, "outside-marker"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../outside-marker", filepath.Join(dir, "data", "dingzi.db-shm")); err != nil {
					t.Fatal(err)
				}
			}
			write("mocks/id", `#!/bin/sh
if [ "$1" = -u ] && [ "$#" = 1 ]; then echo 0; exit 0; fi
if [ "$DINGZI_TEST_FRESH" = true ] && [ ! -f "$DINGZI_INSTALL_TEST/user-created" ]; then exit 1; fi
echo 1234
`)
			write("mocks/useradd", "#!/bin/sh\ntouch \"$DINGZI_INSTALL_TEST/user-created\"\n")
			write("mocks/uname", "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; esac\n")
			write("mocks/chown", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DINGZI_INSTALL_TEST/ownership-calls\"\n")
			write("mocks/sleep", "#!/bin/sh\nexit 0\n")
			if runtime.GOOS == "windows" {
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
[ "$DINGZI_TEST_FAILURE" != download ] || exit 22
[ "$1" = -fsSL ] && [ "$3" = -o ] || exit 45
case "$2" in
  */releases/latest) printf '{"tag_name":"v1.0.0"}\n' > "$4" ;;
  */checksums.txt) cp "$DINGZI_INSTALL_TEST/downloads/checksums.txt" "$4" ;;
  */v1.0.0/dingzi-server-linux-amd64) cp "$DINGZI_INSTALL_TEST/downloads/server" "$4" ;;
  *) exit 46 ;;
esac
`)
			for _, manager := range []string{"systemctl", "rc-service", "rc-update"} {
				write("mocks/"+manager, `#!/bin/sh
printf '%s\n' "$*" >> "$DINGZI_INSTALL_TEST/calls"
for arg in "$@"; do
  [ "$arg" != "$DINGZI_TEST_FAILURE" ] || exit 1
done
`)
			}
			script := string(source)
			for from, to := range map[string]string{
				`BIN_DIR="/usr/local/bin"`:          `BIN_DIR="$DINGZI_INSTALL_TEST/bin"`,
				`CONF_DIR="/etc/dingzi"`:            `CONF_DIR="$DINGZI_INSTALL_TEST/config"`,
				`DATA_DIR="/var/lib/dingzi"`:        `DATA_DIR="$DINGZI_INSTALL_TEST/data"`,
				`SYSTEMD_DIR="/etc/systemd/system"`: `SYSTEMD_DIR="$DINGZI_INSTALL_TEST/systemd"`,
				`INIT_DIR="/etc/init.d"`:            `INIT_DIR="$DINGZI_INSTALL_TEST/init"`,
				`LOG_DIR="/var/log"`:                `LOG_DIR="$DINGZI_INSTALL_TEST/log"`,
				`INIT="$(detect_init)"`:             "INIT=" + tc.init,
			} {
				if strings.Count(script, from) != 1 {
					t.Fatalf("cannot isolate installer setting %s", from)
				}
				script = strings.Replace(script, from, to, 1)
			}
			setup := `if command -v cygpath >/dev/null 2>&1; then
  DINGZI_INSTALL_TEST=$(cygpath -u "$DINGZI_INSTALL_TEST")
fi
export DINGZI_INSTALL_TEST
export PATH="$DINGZI_INSTALL_TEST/mocks:$PATH"
export TMPDIR="$DINGZI_INSTALL_TEST/tmp"
`
			write("installer.sh", setup+script)
			args := append([]string{filepath.ToSlash(filepath.Join(dir, "installer.sh"))}, tc.args...)
			cmd := exec.Command(sh, args...)
			cmd.Env = append(os.Environ(), "DINGZI_INSTALL_TEST="+filepath.ToSlash(dir), "DINGZI_TEST_FAILURE="+tc.failure, fmt.Sprintf("DINGZI_TEST_FRESH=%t", tc.fresh))
			output, err := cmd.CombinedOutput()
			if (err != nil) != (tc.failure != "") {
				t.Fatalf("unexpected install result %v\n%s", err, output)
			}
			if tc.linkedData {
				info, err := os.Stat(filepath.Join(dir, "outside-marker"))
				if err != nil || info.Mode().Perm() != 0o644 || read("outside-marker") != "unchanged\n" {
					t.Fatalf("upgrade modified a data link target: %v", err)
				}
				if strings.Contains(read("ownership-calls"), "/data/") {
					t.Fatal("installer changed ownership through a service-controlled child name")
				}
			}
			if tc.failure != "" && strings.Contains(string(output), "安装完成") {
				t.Fatal("failed installation was reported as complete")
			}
			if !tc.fresh {
				if read("data/config.yaml") != "persisted credentials\n" || read("data/dingzi.db") != "persisted database\n" {
					t.Fatal("installation changed existing credentials or database")
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "executed")); !os.IsNotExist(err) {
				t.Fatal("installer executed configuration as shell code")
			}
			uninstall := len(tc.args) > 0 && tc.args[0] == "--uninstall"
			switch {
			case uninstall && tc.failure == "":
				for _, path := range []string{"bin/dingzi-server", servicePath} {
					if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
						t.Fatalf("uninstall left %s", path)
					}
				}
				if read("config/server.conf") != previousConfig {
					t.Fatal("uninstall changed saved settings")
				}
			case tc.failure == "" || tc.failure == "enable" || tc.failure == "restart" || tc.failure == "is-active":
				if read("bin/dingzi-server") != binary {
					t.Fatal("new binary was not installed")
				}
				want := previousConfig
				if tc.fresh {
					want = "LISTEN=:8008\nSECURE_COOKIE=false\nRETENTION_DAYS=30\n"
				} else if len(tc.args) > 0 {
					want = "LISTEN=[::1]:9009\nSECURE_COOKIE=false\nRETENTION_DAYS=7\n"
				}
				if read("config/server.conf") != want {
					t.Fatal("installation lost settings or ignored overrides")
				}
				service := read(servicePath)
				for _, line := range strings.Split(strings.TrimSpace(want), "\n") {
					_, value, _ := strings.Cut(line, "=")
					if !strings.Contains(service, value) {
						t.Fatalf("service does not use saved value %q", value)
					}
				}
				identity := "User=dingzi-server\nGroup=dingzi-server"
				if tc.init == "openrc" {
					identity = `command_user="dingzi-server:dingzi-server"`
				}
				if !strings.Contains(service, identity) {
					t.Fatal("service does not run as the dedicated user")
				}
				if tc.init == "openrc" {
					// openrc-run evaluates command_args as shell words. Exercise
					// that expansion, including IPv6 brackets, rather than only
					// comparing the generated service text.
					_, listen, _ := strings.Cut(strings.Split(want, "\n")[0], "=")
					inspect := exec.Command(sh, "-c", `. "$1"; expected=$2; eval "set -- $command_args"; [ "$4" = "$expected" ]`, "sh", filepath.ToSlash(filepath.Join(dir, servicePath)), listen)
					inspect.Dir = dir
					if runtime.GOOS != "windows" {
						write("1:9009", "") // Would match an unquoted [::1]:9009.
					}
					if output, err := inspect.CombinedOutput(); err != nil {
						t.Fatalf("OpenRC argument expansion failed: %v\n%s", err, output)
					}
				}
				if tc.fresh {
					if _, err := os.Stat(filepath.Join(dir, "user-created")); err != nil {
						t.Fatal("fresh install did not create service account")
					}
				}
			default:
				if read("bin/dingzi-server") != "previous binary\n" || read("config/server.conf") != previousConfig || read(servicePath) != "previous service\n" {
					t.Fatal("validation failure replaced the existing installation")
				}
			}
			for _, sub := range []string{"bin", "tmp"} {
				entries, err := os.ReadDir(filepath.Join(dir, sub))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if sub == "tmp" || strings.HasPrefix(entry.Name(), ".dingzi-") {
						t.Fatalf("installation left temporary file %s/%s", sub, entry.Name())
					}
				}
			}
		})
	}
}
