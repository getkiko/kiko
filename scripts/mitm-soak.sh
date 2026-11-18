#!/bin/bash
# MITM soak on the build machine. Usage: mitm-soak.sh <core dir> <out dir> [duration]
# Builds the root-package test binary once, then runs the origin, proxy and load roles as three
# processes. The proxy samples its own RSS, goroutines, heap and descriptors into proxy.csv.
set -e
export PATH=$PATH:/usr/local/go/bin
SB=${1:?core dir}; OUT=${2:?out dir}; DUR=${3:-4h}
mkdir -p "$OUT"; rm -f "$OUT"/*.done "$OUT"/*.port "$OUT"/*.pem "$OUT"/*.key
cd "$SB"
[ -x "$OUT/soak.test" ] || go test -c -o "$OUT/soak.test" .
cd "$OUT"
export KIKO_SOAK_DIR="$OUT" KIKO_SOAK_DURATION="$DUR"
KIKO_SOAK_ROLE=origin ./soak.test -test.run '^TestMITMSoak$' -test.timeout 0 > origin.log 2>&1 &
KIKO_SOAK_ROLE=proxy ./soak.test -test.run '^TestMITMSoak$' -test.timeout 0 > proxy.log 2>&1 &
sleep 5
KIKO_SOAK_ROLE=load ./soak.test -test.run '^TestMITMSoak$' -test.timeout 0 > load.log 2>&1 &
wait
