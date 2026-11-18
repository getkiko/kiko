#!/bin/bash
# Script runtime memory cases on the build machine. Usage: script-mem.sh <core dir> <out file>
export PATH=$PATH:/usr/local/go/bin
OUT=$2
cd "$1" || exit 1
go test -c -o /tmp/script-mem.test ./script || exit 1
: > "$OUT"
run() { # case n slots [target]
  KIKO_SCRIPT_MEM_CASE=$1 KIKO_SCRIPT_MEM_N=$2 KIKO_SCRIPT_MEM_SLOTS=$3 KIKO_SCRIPT_MEM_TARGET=${4:-64} timeout 120 /tmp/script-mem.test -test.run '^TestScriptMemoryR1$' -test.v 2>&1 | grep -E "ABORT|RESULT|elapsed=|peak_waiting|body_bytes|json run|panic|fatal|FAIL" >> "$OUT"
  echo "rc=$?" >> "$OUT"
}
for n in 1 10 50 100 256; do run idle $n 0; done
for n in 1 4 8 16 32; do run json $n 0; done
for n in 1 4 8 16; do run httpclient $n 0; done
run httpclient 16 16
run httpclient 32 32
run httpclient 16 16 64
run httpclient-binary 16 16
for c in huge-array array-push array-int-push string-double string-repeat string-concat arraybuffer typed-chain object-churn churn-no-keep regex-backtrack regex-big-match regex-replace-fn split-bomb json-deep json-stringify-bomb map-set closure-chain; do run $c 1 0; done
for c in array-push object-churn string-concat typed-chain; do run $c 8 0; done
run array-push 16 16
echo DONE >> "$OUT"
