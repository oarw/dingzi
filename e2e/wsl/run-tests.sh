#!/bin/sh
# Runs the cross-compiled test binaries inside a Linux userspace.
#
# Exists because the pty only works on unix, and the panel deploys to Linux
# while this is authored on Windows. Copying to /tmp first because the binaries
# live on a mounted Windows filesystem, where the noexec-ish semantics and the
# permission mapping make direct execution unreliable.
set -u

here=$(dirname "$0")
fail=0

for t in proto agent server; do
  bin="$here/$t.test"
  [ -f "$bin" ] || { echo "SKIP $t (no binary)"; continue; }
  cp "$bin" "/tmp/$t.test"
  chmod +x "/tmp/$t.test"
  echo "=== $t ==="
  if (cd /tmp && "./$t.test" -test.timeout 5m 2>&1 | tail -8); then
    :
  else
    fail=1
  fi
done

echo "--- shell environment this ran against ---"
ls -l /bin/sh
/bin/busybox 2>&1 | head -1

exit $fail
